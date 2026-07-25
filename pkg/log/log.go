package log

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type Mode int

const (
	ModeText Mode = iota
	ModeJSON
)

var currentMode = ModeText

func SetMode(m Mode) { currentMode = m }

type Verbosity int

const (
	Quiet Verbosity = iota
	Normal
	Verbose
)

var currentVerbosity = Normal

func SetVerbosity(v Verbosity) { currentVerbosity = v }

type jsonEntry struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	Time    string `json:"time"`
}

func outputJSON(level, format string, a ...interface{}) {
	entry := jsonEntry{
		Level:   level,
		Message: fmt.Sprintf(format, a...),
		Time:    time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, _ := json.Marshal(entry)
	fmt.Fprintln(os.Stderr, string(data))
}

func Info(format string, a ...interface{}) {
	if currentVerbosity == Quiet {
		return
	}
	if currentMode == ModeJSON {
		outputJSON("info", format, a...)
		return
	}
	fmt.Fprintf(os.Stderr, "ℹ [noci] %s\n", fmt.Sprintf(format, a...))
}

func Success(format string, a ...interface{}) {
	if currentMode == ModeJSON {
		outputJSON("success", format, a...)
		return
	}
	fmt.Fprintf(os.Stderr, "✔ [noci] %s\n", fmt.Sprintf(format, a...))
}

func Warning(format string, a ...interface{}) {
	if currentMode == ModeJSON {
		outputJSON("warning", format, a...)
		return
	}
	fmt.Fprintf(os.Stderr, "⚠ [noci] %s\n", fmt.Sprintf(format, a...))
}

func Action(format string, a ...interface{}) {
	if currentVerbosity == Quiet {
		return
	}
	if currentMode == ModeJSON {
		outputJSON("action", format, a...)
		return
	}
	fmt.Fprintf(os.Stderr, "▶ [noci] %s\n", fmt.Sprintf(format, a...))
}

func Debug(format string, a ...interface{}) {
	if currentVerbosity < Verbose {
		return
	}
	if currentMode == ModeJSON {
		outputJSON("debug", format, a...)
		return
	}
	fmt.Fprintf(os.Stderr, "… [noci] %s\n", fmt.Sprintf(format, a...))
}
