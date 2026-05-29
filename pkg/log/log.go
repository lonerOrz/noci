package log

import (
	"fmt"
)

func Info(format string, a ...interface{}) {
	fmt.Printf("ℹ [noci] %s\n", fmt.Sprintf(format, a...))
}

func Success(format string, a ...interface{}) {
	fmt.Printf("✔ [noci] %s\n", fmt.Sprintf(format, a...))
}

func Warning(format string, a ...interface{}) {
	fmt.Printf("⚠ [noci] %s\n", fmt.Sprintf(format, a...))
}

func Action(format string, a ...interface{}) {
	fmt.Printf("▶ [noci] %s\n", fmt.Sprintf(format, a...))
}
