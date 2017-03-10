package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io/ioutil"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"text/template"
	"time"

	"golang.org/x/net/context"

	"github.com/gordonklaus/portaudio"
	"github.com/mattn/go-isatty"
	"github.com/nsf/termbox-go"
	"github.com/raoulh/babymonitor/lame"
	"github.com/raoulh/go-progress"
	"github.com/zenwerk/go-wave"
)

var (
	config Config

	mutexClients  = &sync.Mutex{} //mutex that prevents multiple access of the clientRequest map
	clientRequest map[*http.Request]*Client

	//debug writers
	mp3Writer  *lame.LameWriter
	waveWriter *wave.Writer

	//how much sample we need for mesuring level
	measSampleCount    int
	samplesLevels      []int16
	samplesBufferCount int

	mutexBuff = &sync.Mutex{} //mutex for accessing the buffer

	triggerTimestamp int64
)

const (
	serverUA = "Babymonitor/1.0"

	sampleRate   = 44100
	samplesCount = 128 //number of sample to read each time
)

type Client struct {
	mp3Writer *lame.LameWriter //lame mp3 encoder

	chanEnd chan bool
}

var tmpl = template.Must(template.New("").Parse(
	`{{. | len}} host APIs: {{range .}}
	Name:                   {{.Name}}
	{{if .DefaultInputDevice}}Default input device:   {{.DefaultInputDevice.Name}}{{end}}
	{{if .DefaultOutputDevice}}Default output device:  {{.DefaultOutputDevice.Name}}{{end}}
	Devices: {{range .Devices}}
		Name:                      {{.Name}}
		MaxInputChannels:          {{.MaxInputChannels}}
		MaxOutputChannels:         {{.MaxOutputChannels}}
		DefaultLowInputLatency:    {{.DefaultLowInputLatency}}
		DefaultLowOutputLatency:   {{.DefaultLowOutputLatency}}
		DefaultHighInputLatency:   {{.DefaultHighInputLatency}}
		DefaultHighOutputLatency:  {{.DefaultHighOutputLatency}}
		DefaultSampleRate:         {{.DefaultSampleRate}}
	{{end}}
{{end}}`,
))

type Config struct {
	FFmpegArgs string `json:"ffmpeg_args"`
	Actions    []struct {
		Url     string `json:"url"`
		Type    string `json:"type"`
		Payload string `json:"payload"`
	} `json:"actions"`
	HttpPort int `json:"http_port"`

	DebugMp3 struct {
		Enabled  bool   `json:"enabled"`
		Filename string `json:"filename"`
	} `json:"debug_mp3"`

	DebugWav struct {
		Enabled  bool   `json:"enabled"`
		Filename string `json:"filename"`
	} `json:"debug_wav"`

	LevelTrigger struct {
		MeasureTime int     `json:"measure_time_ms"`
		Level       float64 `json:"level"`
	} `json:"level_trigger"`

	//Time to wait before the trigger can be enabled again
	TriggerPauseSec int64 `json:"trigger_pause_sec"`

	Mp3LameQuality int `json:"mp3_lame_quality"`
}

func readConfig(c string) (err error) {
	log.Println(CharArrow+"Reading config from", c)

	cfile, err := ioutil.ReadFile(c)
	if err != nil {
		log.Println("Reading config file failed")
		return
	}

	if err = json.Unmarshal(cfile, &config); err != nil {
		log.Println("Unmarshal config file failed")
		return
	}

	measSampleCount = config.LevelTrigger.MeasureTime * sampleRate / 1000
	samplesLevels = make([]int16, measSampleCount/samplesCount+measSampleCount%samplesCount)
	triggerTimestamp = time.Now().Unix() - config.TriggerPauseSec

	return
}

func abs(a int16) int16 {
	if a < 0 {
		return -a
	}
	return a
}

func startBabymonitor() (err error) {
	log.Printf("%s Starting baby monitor...", CharStar)

	err = termbox.Init()
	if err != nil {
		panic(err)
	}
	termbox.SetInputMode(termbox.InputEsc)

	portaudio.Initialize()
	defer portaudio.Terminate()

	clientRequest = make(map[*http.Request]*Client)

	hs, err := portaudio.HostApis()
	if err != nil {
		return
	}
	err = tmpl.Execute(os.Stdout, hs)
	if err != nil {
		return
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	inputChannels := 1
	outputChannels := 0
	framesPerBuffer := make([]int16, samplesCount)

	log.Printf("Open default sound input device")
	stream, err := portaudio.OpenDefaultStream(inputChannels, outputChannels, float64(sampleRate), len(framesPerBuffer), framesPerBuffer)
	if err != nil {
		return
	}
	defer stream.Close()

	log.Printf("Start listening")
	err = stream.Start()
	if err != nil {
		return
	}

	if config.DebugWav.Enabled {
		waveFile, err := os.Create(config.DebugWav.Filename)
		if err != nil {
			return err
		}
		defer waveFile.Close()
		param := wave.WriterParam{
			Out:           waveFile,
			Channel:       inputChannels,
			SampleRate:    sampleRate,
			BitsPerSample: 16,
		}
		waveWriter, err = wave.NewWriter(param)
		if err != nil {
			return err
		}
	}

	if config.DebugMp3.Enabled {
		mp3File, err := os.Create(config.DebugMp3.Filename)
		if err != nil {
			return err
		}
		defer mp3File.Close()

		mp3Writer = lame.NewWriter(mp3File)
		setupMp3Parameters(mp3Writer)
	}

	keyChan := make(chan int)
	go func() {
	termMainLoop:
		for {
			switch ev := termbox.PollEvent(); ev.Type {
			case termbox.EventKey:
				switch ev.Key {
				case termbox.KeyEsc:
					keyChan <- 1
					break termMainLoop
				}

			case termbox.EventError:
				panic(ev.Err)

			case termbox.EventInterrupt:
				break termMainLoop
			}
		}
	}()

	var bar *progress.ProgressBar
	if isatty.IsTerminal(os.Stdout.Fd()) {
		bar = progress.New(1000)
		bar.Format = progress.ProgressFormats[8]
	}

	httpSrv := startHttpStreamingServer()

readLoop:
	for {
		err = stream.Read()
		if err != nil {
			log.Println("Failed to read stream")
			termbox.Interrupt()
			break readLoop
		}

		if config.DebugWav.Enabled {
			binary.Write(waveWriter, binary.LittleEndian, framesPerBuffer)
			if err != nil {
				termbox.Interrupt()
				break readLoop
			}
		}

		if config.DebugMp3.Enabled {
			binary.Write(mp3Writer, binary.LittleEndian, framesPerBuffer)
			if err != nil {
				termbox.Interrupt()
				break readLoop
			}
		}

		//Write mp3 to all connected clients
		for _, client := range clientRequest {
			client.writeDataClient(&framesPerBuffer)
		}

		//if we are on a terminal display a nice level bar
		if isatty.IsTerminal(os.Stdout.Fd()) {
			//calc mean value
			var mean uint64
			for _, v := range framesPerBuffer {
				if v < 0 {
					v = -v //Abs
				}
				mean += uint64(v)
			}
			mean /= uint64(len(framesPerBuffer))
			mean = mean * 1000 / math.MaxInt16
			bar.Set(int(mean))
		}

		//Check for level trigger
		processLevelTrigger(framesPerBuffer)

		select {
		case <-sig:
			log.Println("SIGTERM catched")
			termbox.Interrupt()
			break readLoop
		case <-keyChan:
			break readLoop
		default:
		}
	}

	//release all remaining client
	for _, client := range clientRequest {
		client.chanEnd <- true //release client
	}

	//Clean up
	log.Println("Stop. Cleaning...")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err = httpSrv.Shutdown(ctx)

	stream.Stop()

	if config.DebugWav.Enabled {
		waveWriter.Close()
	}

	if config.DebugMp3.Enabled {
		mp3Writer.Close()
	}

	return
}

func startHttpStreamingServer() *http.Server {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	http.Handle("/stream", streamHandler())

	srv := &http.Server{Addr: ":" + strconv.Itoa(config.HttpPort)}

	go func() {
		log.Println("Starting HTTP server, port", config.HttpPort)
		if err := srv.ListenAndServe(); err != nil {
			log.Printf("Httpserver: ListenAndServe() error: %s", err)
		}
	}()

	return srv
}

func streamHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			//Use default go serve handler
			http.DefaultServeMux.ServeHTTP(w, r)
			return
		}

		log.Println("New client for streaming:", r.RemoteAddr)

		w.Header().Set("Server", serverUA)
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(201)

		c := &Client{
			//buffWriter: bufio.NewWriter(w),
			chanEnd: make(chan bool, 1),
		}

		//setup the mp3 writer
		c.mp3Writer = lame.NewWriter(w)
		setupMp3Parameters(c.mp3Writer)

		//Add the client to the list
		mutexClients.Lock()
		clientRequest[r] = c
		mutexClients.Unlock()

		//Wait for an eventual end from writer.
		//If client closes the connection a write error will occur
		//If sound read/mp3 encode is failing, an error will occur
		//and client will need to quit
		<-clientRequest[r].chanEnd

		mutexClients.Lock()
		delete(clientRequest, r)
		mutexClients.Unlock()

		log.Println("Closing HTTP client:", r.RemoteAddr)
	})
}

func (c *Client) writeDataClient(pcmData *[]int16) {
	if err := binary.Write(c.mp3Writer, binary.LittleEndian, pcmData); err != nil {
		log.Println("Failed to write data to client", err)
		c.chanEnd <- true //release client
	}
}

func processLevelTrigger(pcmData []int16) {
	mutexBuff.Lock()
	defer mutexBuff.Unlock()

	if samplesBufferCount >= len(samplesLevels) {
		//The buffer is already filled and another goroutine is checking the data
		//drop those data samples
		return
	}

	//Copy data to our buffer
	start := samplesBufferCount / samplesCount
	for i, _ := range pcmData {
		samplesLevels[start+i] = pcmData[i]
	}

	//increment counter
	samplesBufferCount += samplesCount

	if samplesBufferCount >= len(samplesLevels) {
		go checkLevels()
	}
}

func checkLevels() {
	//Calc the mean for the full range and if the level is >= of configured one, trigger actions
	var mean float64
	for _, v := range samplesLevels {
		if v < 0 {
			v = -v //Abs
		}
		mean += float64(v) / math.MaxInt16
	}
	mean /= float64(len(samplesLevels))

	t := time.Now().Unix() - triggerTimestamp

	if mean >= config.LevelTrigger.Level &&
		t >= config.TriggerPauseSec {
		log.Println("Level triggerd with:", mean, ". Calling actions.")
		for _, action := range config.Actions {
			go callAction(action.Type, action.Url, []byte(action.Payload))
		}

		triggerTimestamp = time.Now().Unix()
	}

	mutexBuff.Lock()
	defer mutexBuff.Unlock()

	samplesBufferCount = 0
}

func callAction(reqtype string, url string, data []byte) (_ []byte, err error) {
	req, err := http.NewRequest(reqtype, url, bytes.NewBuffer(data))

	log.Println("Call action:", url)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Println("Failed to call request to", url, err)
		return nil, err
	}
	defer resp.Body.Close()

	log.Println("Response Status:", resp.Status)
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, err
	}

	return ioutil.ReadAll(resp.Body)
}

func setupMp3Parameters(w *lame.LameWriter) {
	w.Encoder.SetNumChannels(1)
	w.Encoder.SetInSamplerate(sampleRate)
	w.Encoder.SetMode(lame.MONO)
	w.Encoder.SetQuality(config.Mp3LameQuality)
	w.Encoder.InitParams()
}
