package nix

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ExportAndCompress exports a store path and compresses it as a NAR archive.
func ExportAndCompress(ctx context.Context, storePath string, comp string, concurrency int, level int) (tempFile string, fileHash string, fileSize int64, err error) {
	c, err := GetCompressor(comp)
	if err != nil {
		return "", "", 0, err
	}

	tmp, err := os.CreateTemp("", "noci-nar-*"+c.Extension())
	if err != nil {
		return "", "", 0, err
	}
	defer func() {
		if tmp != nil {
			_ = tmp.Close()
		}
	}()

	bufWriter := bufio.NewWriterSize(tmp, 256*1024)

	hashWriter := sha256.New()
	multiWriter := io.MultiWriter(bufWriter, hashWriter)

	compressor, err := c.WrapWriter(multiWriter, concurrency, level)
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		tmp = nil
		return "", "", 0, err
	}

	defer func() {
		if compressor != nil {
			_ = compressor.Close()
		}
	}()

	dumpCmd := exec.CommandContext(ctx, "nix-store", "--dump", storePath)
	dumpCmd.Stdout = compressor
	var errBuf strings.Builder
	dumpCmd.Stderr = &errBuf

	if err := dumpCmd.Run(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		tmp = nil
		if stderr := strings.TrimSpace(errBuf.String()); stderr != "" {
			return "", "", 0, fmt.Errorf("nix-store dump failed: %w (stderr: %s)", err, stderr)
		}
		return "", "", 0, fmt.Errorf("nix-store dump failed: %w", err)
	}

	_ = compressor.Close()
	compressor = nil

	_ = bufWriter.Flush()

	stat, err := tmp.Stat()
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		tmp = nil
		return "", "", 0, err
	}

	_ = tmp.Close()
	tempName := tmp.Name()
	tmp = nil

	return tempName, hex.EncodeToString(hashWriter.Sum(nil)), stat.Size(), nil
}

func GenerateNarInfo(storePath, narHash string, narSize int64, fileHash string, fileSize int64, refs []string, sigs []string, comp string) string {
	c, _ := GetCompressor(comp)

	var refBasenames []string
	for _, r := range refs {
		refBasenames = append(refBasenames, filepath.Base(r))
	}

	lines := []string{
		"StorePath: " + storePath,
		"URL: nar/" + GetPathHash(storePath) + c.Extension(),
		"Compression: " + c.Name(),
		"FileHash: sha256:" + fileHash,
		"FileSize: " + fmt.Sprintf("%d", fileSize),
		"NarHash: " + narHash,
		"NarSize: " + fmt.Sprintf("%d", narSize),
	}

	if len(refBasenames) > 0 {
		lines = append(lines, "References: "+strings.Join(refBasenames, " "))
	}
	for _, sig := range sigs {
		lines = append(lines, "Sig: "+sig)
	}

	return strings.Join(lines, "\n") + "\n"
}
