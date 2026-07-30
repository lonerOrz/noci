package types

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var nixHashRegex = regexp.MustCompile(`^[0-9abcdfghijklmnpqrsvwxyz]{32}$`)

type NixHash string

func ParseNixHash(s string) (NixHash, error) {
	cleaned := strings.ToLower(strings.TrimSpace(s))
	if !nixHashRegex.MatchString(cleaned) {
		return "", fmt.Errorf("invalid nix base32 hash: %q", s)
	}
	return NixHash(cleaned), nil
}

func (h NixHash) String() string { return string(h) }

type OciDigest string

func ParseOciDigest(s string) (OciDigest, error) {
	raw := strings.TrimSpace(s)
	hex := strings.TrimPrefix(raw, "sha256:")
	if len(hex) != 64 {
		return "", fmt.Errorf("invalid sha256 hex length in digest: %q", s)
	}
	return OciDigest("sha256:" + strings.ToLower(hex)), nil
}

func (d OciDigest) BareHex() string {
	return strings.TrimPrefix(string(d), "sha256:")
}

func (d OciDigest) String() string { return string(d) }

type StorePath string

func ParseStorePath(p string) (StorePath, error) {
	cleaned := filepath.Clean(strings.TrimSpace(p))
	if !strings.HasPrefix(cleaned, "/nix/store/") {
		return "", fmt.Errorf("not a valid nix store path: %q", p)
	}
	base := filepath.Base(cleaned)
	if len(base) < 33 || base[32] != '-' {
		return "", fmt.Errorf("invalid store path format: %q", p)
	}
	return StorePath(cleaned), nil
}

func (sp StorePath) Hash() NixHash {
	base := filepath.Base(string(sp))
	return NixHash(base[:32])
}

func (sp StorePath) Name() string {
	base := filepath.Base(string(sp))
	return base[33:]
}

func (sp StorePath) String() string { return string(sp) }
