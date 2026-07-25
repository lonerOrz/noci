package config

import "testing"

func TestParseTTL(t *testing.T) {
	tests := []struct {
		input string
		want  int64
		err   bool
	}{
		{"0", 0, false},
		{"30d", 30 * 24 * 3600, false},
		{"1d", 86400, false},
		{"24h", 86400, false},
		{"90m", 5400, false},
		{"1h30m", 5400, false},
		{"3600s", 3600, false},
		{"invalid", 0, true},
		{"xd", 0, true},
	}

	for _, tt := range tests {
		got, err := ParseTTL(tt.input)
		if (err != nil) != tt.err {
			t.Errorf("ParseTTL(%q) error = %v, wantErr %v", tt.input, err, tt.err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseTTL(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestParseSize(t *testing.T) {
	tests := []struct {
		input string
		want  int64
		err   bool
	}{
		{"", 0, false},
		{"100", 100, false},
		{"100B", 100, false},
		{"1KB", 1024, false},
		{"1K", 1024, false},
		{"1MB", 1024 * 1024, false},
		{"1M", 1024 * 1024, false},
		{"1GB", 1024 * 1024 * 1024, false},
		{"1G", 1024 * 1024 * 1024, false},
		{"1TB", 1024 * 1024 * 1024 * 1024, false},
		{"1T", 1024 * 1024 * 1024 * 1024, false},
		{"5mb", 5 * 1024 * 1024, false},
		{"  2 GB  ", 2 * 1024 * 1024 * 1024, false},
		{"abc", 0, true},
	}

	for _, tt := range tests {
		got, err := ParseSize(tt.input)
		if (err != nil) != tt.err {
			t.Errorf("ParseSize(%q) error = %v, wantErr %v", tt.input, err, tt.err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseSize(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestIsNixHash(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"0123456789abcdf0123456789abcdf01", true},   // 32 valid chars
		{"abcdfghijklmnpqrsvwxyz0123456789", true},   // 32 valid chars
		{"0123456789abcdf0123456789abcdf0", false},   // 31 chars
		{"0123456789abcdf0123456789abcdf012", false}, // 33 chars
		{"", false},
		{"hello world", false},
		{"0123456789ABCDF0123456789ABCDF01", true}, // uppercase should match
	}

	for _, tt := range tests {
		got := IsNixHash(tt.input)
		if got != tt.want {
			t.Errorf("IsNixHash(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
