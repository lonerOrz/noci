package nix

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"
)

func TestNewSignerFromKey(t *testing.T) {
	// Generate a real ed25519 key pair
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = pub

	keyContent := "noci-cache:" + base64.StdEncoding.EncodeToString(priv)
	signer, err := NewSignerFromKey(keyContent)
	if err != nil {
		t.Fatalf("NewSignerFromKey failed: %v", err)
	}
	if signer.KeyName != "noci-cache" {
		t.Errorf("KeyName = %q, want noci-cache", signer.KeyName)
	}
}

func TestNewSignerFromKey_InvalidFormat(t *testing.T) {
	_, err := NewSignerFromKey("no-colon")
	if err == nil {
		t.Error("expected error for format without colon")
	}
}

func TestNewSignerFromKey_BadBase64(t *testing.T) {
	_, err := NewSignerFromKey("key:!!!not-base64!!!")
	if err == nil {
		t.Error("expected error for invalid base64")
	}
}

func TestNormalizeNarHash_AlreadyNix(t *testing.T) {
	got, err := NormalizeNarHash("sha256:abc123")
	if err != nil {
		t.Fatal(err)
	}
	if got != "sha256:abc123" {
		t.Errorf("got %q, want sha256:abc123", got)
	}
}

func TestNormalizeNarHash_SRIToNix(t *testing.T) {
	// sha256-AAAA... (32 bytes of 0) in base64
	zeros := make([]byte, 32)
	sri := "sha256-" + base64.RawStdEncoding.EncodeToString(zeros)
	got, err := NormalizeNarHash(sri)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 32+7 { // "sha256:" + 25 nix32 chars for 32 bytes
		// 32 bytes → 52 nix32 chars
		// Actually: "sha256:" + 52 chars = 59 chars
	}
	if got[:7] != "sha256:" {
		t.Errorf("prefix = %q", got[:7])
	}
	// Verify the nix32 part is all valid characters
	nix32Part := got[7:]
	validChars := "0123456789abcdfghijklmnpqrsvwxyz"
	for _, c := range nix32Part {
		found := false
		for _, v := range validChars {
			if c == v {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("invalid nix32 char %q in %q", c, nix32Part)
		}
	}
}

func TestNormalizeNarHash_UnknownFormat(t *testing.T) {
	_, err := NormalizeNarHash("md5:abc123")
	if err == nil {
		t.Error("expected error for unknown format")
	}
}

func TestNormalizeNarHash_SRIBadBase64(t *testing.T) {
	_, err := NormalizeNarHash("sha256-!!!invalid!!!")
	if err == nil {
		t.Error("expected error for bad SRI base64")
	}
}

func TestNormalizeNarHash_SRIBadLength(t *testing.T) {
	// 16 bytes instead of 32
	short := make([]byte, 16)
	sri := "sha256-" + base64.RawStdEncoding.EncodeToString(short)
	_, err := NormalizeNarHash(sri)
	if err == nil {
		t.Error("expected error for wrong byte length")
	}
}

func TestHexToNixBase32(t *testing.T) {
	// SHA-256("test") hex
	hexInput := "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	got, err := HexToNixBase32(hexInput)
	if err != nil {
		t.Fatalf("HexToNixBase32: %v", err)
	}
	if len(got) != 52 {
		t.Errorf("len = %d, want 52", len(got))
	}
	for _, c := range got {
		found := false
		for _, v := range NixAlphabet {
			if c == v {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("invalid nix32 char %q", c)
		}
	}
}

func TestHexToNixBase32_BadHex(t *testing.T) {
	_, err := HexToNixBase32("not-hex")
	if err == nil {
		t.Error("expected error for invalid hex")
	}
}

func TestHexToNixBase32_WrongLength(t *testing.T) {
	_, err := HexToNixBase32("aabb")
	if err == nil {
		t.Error("expected error for wrong length")
	}
}

func TestEncodeNixBase32(t *testing.T) {
	// Test with known 32-byte input
	input := make([]byte, 32)
	for i := range input {
		input[i] = byte(i)
	}
	got := encodeNixBase32(input)
	if len(got) != 52 {
		t.Errorf("encodeNixBase32(32 bytes) = %d chars, want 52", len(got))
	}
}

func TestEncodeNixBase32_Deterministic(t *testing.T) {
	input := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
	got1 := encodeNixBase32(input)
	got2 := encodeNixBase32(input)
	if got1 != got2 {
		t.Errorf("not deterministic: %q != %q", got1, got2)
	}
}

func TestSignPath_Deterministic(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	keyContent := "test-key:" + base64.StdEncoding.EncodeToString(priv)
	signer, _ := NewSignerFromKey(keyContent)

	storePath := "/nix/store/abc123-test-pkg"
	narHash := "sha256:abcdef0123456789"
	var narSize int64 = 1024
	refs := []string{"/nix/store/def456-dep"}

	sig1, err := signer.SignPath(storePath, narHash, narSize, refs)
	if err != nil {
		t.Fatal(err)
	}
	sig2, err := signer.SignPath(storePath, narHash, narSize, refs)
	if err != nil {
		t.Fatal(err)
	}
	if sig1 != sig2 {
		t.Errorf("non-deterministic signatures: %q != %q", sig1, sig2)
	}
}

func TestSignPath_Format(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	keyContent := "my-key:" + base64.StdEncoding.EncodeToString(priv)
	signer, _ := NewSignerFromKey(keyContent)

	sig, err := signer.SignPath("/nix/store/aaa-pkg", "sha256:bbb", 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sig[:7] != "my-key:" {
		t.Errorf("signature prefix = %q, want my-key:", sig[:7])
	}
}
