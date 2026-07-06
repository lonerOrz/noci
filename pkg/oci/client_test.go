package oci

import (
	"testing"
)

func TestFormatSize(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
		{1099511627776, "1.0 TB"},
	}

	for _, tt := range tests {
		got := FormatSize(tt.input)
		if got != tt.want {
			t.Errorf("FormatSize(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseNextLink(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple next link",
			input: `</v2/user/repo/nix-cache/tags/list?n=100&last=abc>; rel="next"`,
			want:  "/tags/list?n=100&last=abc",
		},
		{
			name:  "multiple links",
			input: `</first>; rel="prev", </v2/user/repo/nix-cache/tags/list?n=100&last=abc>; rel="next"`,
			want:  "/tags/list?n=100&last=abc",
		},
		{
			name:  "no next link",
			input: `</first>; rel="prev"`,
			want:  "",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseNextLink(tt.input)
			if got != tt.want {
				t.Errorf("parseNextLink(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
