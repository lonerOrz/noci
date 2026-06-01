package log

import (
	"fmt"
	"os"
)

func Info(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "ℹ [noci] %s\n", fmt.Sprintf(format, a...))
}

func Success(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "✔ [noci] %s\n", fmt.Sprintf(format, a...))
}

func Warning(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "⚠ [noci] %s\n", fmt.Sprintf(format, a...))
}

func Action(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "▶ [noci] %s\n", fmt.Sprintf(format, a...))
}
