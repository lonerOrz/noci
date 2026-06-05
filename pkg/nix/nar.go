package nix

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// ExportAndCompress 流式将 nix-store 导出并动态选择压缩算法 (Context-Aware)
// concurrency: zstd 编码器线程数，<=0 时自动设为 min(4, max(1, NumCPU/2))
// level: zstd 压缩等级 (1-19)，<=0 时默认 3
func ExportAndCompress(ctx context.Context, storePath string, comp string, concurrency int, level int) (tempFile string, fileHash string, fileSize int64, err error) {
	ext := ".nar.gz"
	if comp == "zstd" {
		ext = ".nar.zst"
	}

	tmp, err := os.CreateTemp("", "noci-nar-*"+ext)
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

	var compressor io.WriteCloser
	if comp == "zstd" {
		if concurrency <= 0 {
			concurrency = runtime.NumCPU() / 2
			if concurrency < 1 {
				concurrency = 1
			} else if concurrency > 4 {
				concurrency = 4
			}
		}
		if level <= 0 {
			level = 3
		}
		compressor, err = zstd.NewWriter(multiWriter, zstd.WithEncoderConcurrency(concurrency), zstd.WithEncoderLevel(zstd.EncoderLevel(level)))
	} else {
		compressor = gzip.NewWriter(multiWriter)
	}
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

	if err := dumpCmd.Run(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		tmp = nil
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
	ext := ".nar.gz"
	compName := "gzip"
	if comp == "zstd" {
		ext = ".nar.zst"
		compName = "zstd"
	}

	var refBasenames []string
	for _, r := range refs {
		refBasenames = append(refBasenames, filepath.Base(r))
	}

	lines := []string{
		"StorePath: " + storePath,
		"URL: nar/" + GetPathHash(storePath) + ext,
		"Compression: " + compName,
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
