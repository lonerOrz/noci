package log

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func captureStderr(fn func()) string {
	orig := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	fn()

	w.Close()
	os.Stderr = orig

	var buf bytes.Buffer
	buf.ReadFrom(r)
	return buf.String()
}

func TestTextMode(t *testing.T) {
	SetMode(ModeText)
	defer SetMode(ModeText)

	output := captureStderr(func() {
		Info("hello %s", "world")
	})

	if !strings.Contains(output, "ℹ [noci] hello world") {
		t.Errorf("Info output = %q, expected text format", output)
	}
}

func TestTextMode_Success(t *testing.T) {
	SetMode(ModeText)
	defer SetMode(ModeText)

	output := captureStderr(func() {
		Success("done")
	})

	if !strings.Contains(output, "✔ [noci] done") {
		t.Errorf("Success output = %q", output)
	}
}

func TestTextMode_Warning(t *testing.T) {
	SetMode(ModeText)
	defer SetMode(ModeText)

	output := captureStderr(func() {
		Warning("oops")
	})

	if !strings.Contains(output, "⚠ [noci] oops") {
		t.Errorf("Warning output = %q", output)
	}
}

func TestTextMode_Action(t *testing.T) {
	SetMode(ModeText)
	defer SetMode(ModeText)

	output := captureStderr(func() {
		Action("working")
	})

	if !strings.Contains(output, "▶ [noci] working") {
		t.Errorf("Action output = %q", output)
	}
}

func TestJSONMode_Info(t *testing.T) {
	SetMode(ModeJSON)
	defer SetMode(ModeText)

	output := captureStderr(func() {
		Info("hello %s", "world")
	})

	output = strings.TrimSpace(output)
	var entry jsonEntry
	if err := json.Unmarshal([]byte(output), &entry); err != nil {
		t.Fatalf("invalid JSON output: %v\nraw: %q", err, output)
	}
	if entry.Level != "info" {
		t.Errorf("level = %q, want info", entry.Level)
	}
	if entry.Message != "hello world" {
		t.Errorf("message = %q", entry.Message)
	}
	if entry.Time == "" {
		t.Error("time should not be empty")
	}
}

func TestJSONMode_Success(t *testing.T) {
	SetMode(ModeJSON)
	defer SetMode(ModeText)

	output := captureStderr(func() {
		Success("completed")
	})

	output = strings.TrimSpace(output)
	var entry jsonEntry
	if err := json.Unmarshal([]byte(output), &entry); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if entry.Level != "success" {
		t.Errorf("level = %q, want success", entry.Level)
	}
}

func TestJSONMode_Warning(t *testing.T) {
	SetMode(ModeJSON)
	defer SetMode(ModeText)

	output := captureStderr(func() {
		Warning("careful")
	})

	output = strings.TrimSpace(output)
	var entry jsonEntry
	if err := json.Unmarshal([]byte(output), &entry); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if entry.Level != "warning" {
		t.Errorf("level = %q, want warning", entry.Level)
	}
}

func TestJSONMode_Action(t *testing.T) {
	SetMode(ModeJSON)
	defer SetMode(ModeText)

	output := captureStderr(func() {
		Action("uploading")
	})

	output = strings.TrimSpace(output)
	var entry jsonEntry
	if err := json.Unmarshal([]byte(output), &entry); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if entry.Level != "action" {
		t.Errorf("level = %q, want action", entry.Level)
	}
}
