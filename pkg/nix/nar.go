package nix

import (
	"compress/gzip"
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

// ExportAndCompress 流式将 nix-store 导出并采用 Gzip 进行高效压缩 (Context-Aware)
func ExportAndCompress(ctx context.Context, storePath string) (tempFile string, fileHash string, fileSize int64, err error) {
	tmp, err := os.CreateTemp("", "noci-nar-*.nar.gz")
	if err != nil {
		return "", "", 0, err
	}
	defer tmp.Close()

	hashWriter := sha256.New()
	multiWriter := io.MultiWriter(tmp, hashWriter)

	gzipWriter := gzip.NewWriter(multiWriter)

	// 使用 CommandContext 启动，保障超时或中断时无进程泄漏
	dumpCmd := exec.CommandContext(ctx, "nix-store", "--dump", storePath)
	dumpCmd.Stdout = gzipWriter

	if err := dumpCmd.Run(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", "", 0, fmt.Errorf("nix-store dump failed: %w", err)
	}

	_ = gzipWriter.Close()

	stat, err := tmp.Stat()
	if err != nil {
		_ = os.Remove(tmp.Name())
		return "", "", 0, err
	}

	return tmp.Name(), hex.EncodeToString(hashWriter.Sum(nil)), stat.Size(), nil
}

func GenerateNarInfo(storePath, narHash string, narSize int64, fileHash string, fileSize int64, refs []string, sigs []string) string {
	var refBasenames []string
	for _, r := range refs {
		refBasenames = append(refBasenames, filepath.Base(r))
	}

	lines := []string{
		"StorePath: " + storePath,
		"URL: nar/" + GetPathHash(storePath) + ".nar.gz",
		"Compression: gzip",
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
