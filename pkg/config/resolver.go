package config

import (
	"noci/pkg/domain/types"
)

// IsNixHash returns true if s is a valid 32-char Nix base32 hash.
func IsNixHash(s string) bool {
	_, err := types.ParseNixHash(s)
	return err == nil
}
