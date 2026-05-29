package nix

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Signer struct {
	KeyName    string
	PrivateKey ed25519.PrivateKey
}

// NewSigner 从文件路径加载 Nix 私钥签名器
func NewSigner(keyPath string) (*Signer, error) {
	bytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	return NewSignerFromKey(string(bytes))
}

// NewSignerFromKey 直接从密钥文本内容加载私钥签名器（适合从环境变量加载）
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

// SignPath 计算 Nix 签名指纹并返回签名行
func (s *Signer) SignPath(storePath, narHash string, narSize int64, refs []string) (string, error) {
	var refPaths []string
	for _, r := range refs {
		refPaths = append(refPaths, "/nix/store/"+filepath.Base(r))
	}

	// Nix 校验指纹格式： 1;<storePath>;<narHash>;<narSize>;<commaRefs>
	fingerprint := fmt.Sprintf("1;%s;%s;%d;%s",
		storePath, narHash, narSize, strings.Join(refPaths, ","))

	sigBytes := ed25519.Sign(s.PrivateKey, []byte(fingerprint))
	encodedSig := base64.StdEncoding.EncodeToString(sigBytes)

	return fmt.Sprintf("%s:%s", s.KeyName, encodedSig), nil
}
