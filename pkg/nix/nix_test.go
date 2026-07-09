package nix

import (
	"testing"
)

func TestGetPathHash(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/nix/store/0abc1234567890abc1234567890abc12-hello", "0abc1234567890abc1234567890abc12"},
		{"/nix/store/short", ""},
		{"/nix/store/", ""},
		{"just-a-name", ""},
	}

	for _, tt := range tests {
		got := GetPathHash(tt.input)
		if got != tt.want {
			t.Errorf("GetPathHash(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestGetPathName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/nix/store/abc1234567890abc1234567890abcdef-hello-world", "hello-world"},
		{"/nix/store/abc1234567890abc1234567890abcdef-", "abc1234567890abc1234567890abcdef-"},
		{"/nix/store/short", "short"},
	}

	for _, tt := range tests {
		got := GetPathName(tt.input)
		if got != tt.want {
			t.Errorf("GetPathName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseJSONBuildOutputs(t *testing.T) {
	input := []byte(`[{"outputs":{"out":"/nix/store/abc-hello"}}]`)
	paths, err := ParseJSONBuildOutputs(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "/nix/store/abc-hello" {
		t.Errorf("paths = %v", paths)
	}
}

func TestParseJSONBuildOutputs_MultipleOutputs(t *testing.T) {
	input := []byte(`[{"outputs":{"out":"/nix/store/abc-pkg","lib":"/nix/store/def-lib"}}]`)
	paths, err := ParseJSONBuildOutputs(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Errorf("paths count = %d, want 2", len(paths))
	}
}

func TestParseJSONBuildOutputs_Empty(t *testing.T) {
	input := []byte(`[]`)
	paths, err := ParseJSONBuildOutputs(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Errorf("paths = %v, want empty", paths)
	}
}

func TestParseJSONBuildOutputs_InvalidJSON(t *testing.T) {
	_, err := ParseJSONBuildOutputs([]byte(`not json`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestGetPathHash_EdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"standard store path", "/nix/store/0abc1234567890abc1234567890abc12-hello", "0abc1234567890abc1234567890abc12"},
		{"trailing slash", "/nix/store/0abc1234567890abc1234567890abc12-hello/", "0abc1234567890abc1234567890abc12"},
		{"double slash", "/nix/store//0abc1234567890abc1234567890abc12-hello", "0abc1234567890abc1234567890abc12"},
		{"relative dot segments", "/nix/store/./0abc1234567890abc1234567890abc12-hello/.", "0abc1234567890abc1234567890abc12"},
		{"trailing slash on short path", "/nix/store/short/", ""},
		{"dot-only path", "/nix/store/.", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetPathHash(tt.input)
			if got != tt.want {
				t.Errorf("GetPathHash(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseJSONBuildOutputs_NoOutputs(t *testing.T) {
	input := []byte(`[{"other":"field"}]`)
	paths, err := ParseJSONBuildOutputs(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Errorf("paths = %v, want empty", paths)
	}
}
