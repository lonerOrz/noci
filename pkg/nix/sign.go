package nix

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Signer struct {
	KeyName    string
	PrivateKey ed25519.PrivateKey
}

func NewSigner(keyPath string) (*Signer, error) {
	bytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	return NewSignerFromKey(string(bytes))
}

func NewSignerFromKey(content string) (*Signer, error) {
	content = strings.TrimSpace(content)
	parts := strings.Split(content, ":")

	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid nix private key format")
	}

	rawKey, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("failed to decode private key base64: %w", err)
	}

	return &Signer{
		KeyName:    parts[0],
		PrivateKey: ed25519.PrivateKey(rawKey),
	}, nil
}

// SignPath 根据标准 Nix 指纹协议对 Store 路径计算其加签签名
func (s *Signer) SignPath(storePath, narHash string, narSize int64, refs []string) (string, error) {
	var refPaths []string
	for _, r := range refs {
		refPaths = append(refPaths, "/nix/store/"+filepath.Base(r))
	}

	sort.Strings(refPaths)

	// Nix 校验指纹格式： 1;<storePath>;<narHash>;<narSize>;<commaRefs>
	fingerprint := fmt.Sprintf("1;%s;%s;%d;%s",
		storePath, narHash, narSize, strings.Join(refPaths, ","))

	sigBytes := ed25519.Sign(s.PrivateKey, []byte(fingerprint))
	encodedSig := base64.StdEncoding.EncodeToString(sigBytes)

	return fmt.Sprintf("%s:%s", s.KeyName, encodedSig), nil
}

const NixAlphabet = "0123456789abcdfghijklmnpqrsvwxyz"

func IsValidNixHash(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') ||
			(c >= 'a' && c <= 'z' && c != 'e' && c != 'o' && c != 't' && c != 'u')) {
			return false
		}
	}
	return true
}

// NormalizeNarHash 将任意 "sha256-xxxx..."（SRI）格式无损转换为标准 "sha256:xxxx..."（Nix Base32）
func NormalizeNarHash(hash string) (string, error) {
	if strings.HasPrefix(hash, "sha256:") {
		return hash, nil
	}
	if strings.HasPrefix(hash, "sha256-") {
		b64 := strings.TrimPrefix(hash, "sha256-")
		if len(b64)%4 != 0 {
			b64 += strings.Repeat("=", 4-(len(b64)%4))
		}
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return "", fmt.Errorf("failed to decode SRI hash: %w", err)
		}
		if len(data) != 32 {
			return "", fmt.Errorf("invalid sha256 byte length: %d", len(data))
		}
		return "sha256:" + encodeNixBase32(data), nil
	}
	return "", fmt.Errorf("unknown hash format: %s", hash)
}

func encodeNixBase32(b []byte) string {
	l := (len(b)*8 + 4) / 5
	out := make([]byte, l)
	for i := 0; i < l; i++ {
		bNum := i * 5
		c := bNum / 8
		bit := bNum % 8
		var val byte
		if c < len(b) {
			val = b[c] >> bit
		}
		if bit > 3 && c+1 < len(b) {
			val |= b[c+1] << (8 - bit)
		}
		out[i] = NixAlphabet[val&0x1f]
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}
