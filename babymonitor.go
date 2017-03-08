package main

import (
	"encoding/json"
	"io/ioutil"
	"log"

	"github.com/gordonklaus/portaudio"
)

var (
	config Config
)

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

func startBabymonitor() (err error) {
	log.Printf("%s Starting baby monitor...", CharStar)

	portaudio.Initialize()
	defer portaudio.Terminate()

	devs, err := portaudio.Devices()
	for _, d := range devs {
		log.Printf("Device: %s  API: %s", d.Name, d.HostApi.Name)
	}

	return
}
