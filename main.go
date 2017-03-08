// +build go1.7

package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/fatih/color"
	"github.com/jawher/mow.cli"
	"github.com/k-takata/go-iscygpty"
	"github.com/mattn/go-isatty"
)

const (
	CharStar     = "\u2737"
	CharAbort    = "\u2718"
	CharCheck    = "\u2714"
	CharWarning  = "\u26A0"
	CharArrow    = "\u2012\u25b6"
	CharVertLine = "\u2502"
)

var (
	blue       = color.New(color.FgBlue).SprintFunc()
	errorRed   = color.New(color.FgRed).SprintFunc()
	errorBgRed = color.New(color.BgRed, color.FgBlack).SprintFunc()
	green      = color.New(color.FgGreen).SprintFunc()
	cyan       = color.New(color.FgCyan).SprintFunc()
	bgCyan     = color.New(color.FgWhite).SprintFunc()
)

var (
	optConfig  *string
	isTerminal bool
)

func exit(err error, exit int) {
	fmt.Fprintln(os.Stderr, errorRed(CharAbort), err)
	cli.Exit(exit)
}

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())

	// fix for cygwin terminal
	if iscygpty.IsCygwinPty(os.Stdout.Fd()) || isatty.IsTerminal(os.Stdout.Fd()) {
		isTerminal = true
	}

	app := cli.App("Babymonitor", "Tool that listen on a microphone and triggers an url when a level has been reached for too long. It also streams the sound in MP3.")

	optConfig = app.StringArg("CONFIG", "babymonitor.conf", "Config file to use")
	app.Spec = "[CONFIG]"
	app.Action = func() {
		err := readConfig(*optConfig)
		if err != nil {
			exit(err, 1)
		}

		err = startBabymonitor()
		if err != nil {
			exit(err, 1)
		}
	}

	if err := app.Run(os.Args); err != nil {
		exit(err, 1)
	}
}
