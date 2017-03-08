package main

import (
	"encoding/binary"
	"encoding/json"
	_ "fmt"
	"io/ioutil"
	"log"
	"math"
	"os"
	"os/signal"
	"text/template"

	"github.com/gordonklaus/portaudio"
	"github.com/nsf/termbox-go"
	"github.com/raoulh/babymonitor/lame"
	"github.com/raoulh/go-progress"
	wave "github.com/zenwerk/go-wave"
)

var (
	config Config
)

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
	FFmpegArgs string   `json:"ffmpeg_args"`
	actions    []string `json:"actions"`
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

	hs, err := portaudio.HostApis()
	if err != nil {
		return
	}
	err = tmpl.Execute(os.Stdout, hs)
	if err != nil {
		return
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, os.Kill)

	inputChannels := 1
	outputChannels := 0
	sampleRate := 44100
	framesPerBuffer := make([]int16, 10)

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

	waveFile, err := os.Create("test.wav")
	if err != nil {
		return
	}
	defer waveFile.Close()
	param := wave.WriterParam{
		Out:           waveFile,
		Channel:       inputChannels,
		SampleRate:    sampleRate,
		BitsPerSample: 16,
	}
	waveWriter, err := wave.NewWriter(param)
	if err != nil {
		return
	}

	mp3File, err := os.Create("output.mp3")
	if err != nil {
		return
	}
	defer mp3File.Close()

	mp3Writer := lame.NewWriter(mp3File)
	mp3Writer.Encoder.SetNumChannels(1)
	mp3Writer.Encoder.SetInSamplerate(sampleRate)
	mp3Writer.Encoder.InitParams()

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

	bar := progress.New(1000)
	bar.Format = progress.ProgressFormats[8]

readLoop:
	for {
		err = stream.Read()
		if err != nil {
			log.Println("Failed to read stream")
			termbox.Interrupt()
			break readLoop
		}

		//binary.Write(f, binary.BigEndian, in)
		//		_, err := waveWriter.Write([]byte(framesPerBuffer))

		binary.Write(waveWriter, binary.LittleEndian, framesPerBuffer)
		binary.Write(mp3Writer, binary.LittleEndian, framesPerBuffer)
		if err != nil {
			termbox.Interrupt()
			break readLoop
		}
		//		fmt.Println(framesPerBuffer)

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

	//Clean up
	log.Println("Stop. Cleaning...")
	stream.Stop()
	waveWriter.Close()
	mp3Writer.Close()

	return
}
