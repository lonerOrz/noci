package nix

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ExportAndCompress 流式将 nix-store 导出并采用 Gzip 进行高效压缩
func ExportAndCompress(storePath string) (tempFile string, fileHash string, fileSize int64, err error) {
	// 创建临时文件来临时保存压缩归档
	tmp, err := os.CreateTemp("", "noci-nar-*.nar.gz")
	if err != nil {
		return "", "", 0, err
	}
	defer tmp.Close()

	hashWriter := sha256.New()
	// 复合写入器：同时计算 SHA256 并写入磁盘
	multiWriter := io.MultiWriter(tmp, hashWriter)

	gzipWriter := gzip.NewWriter(multiWriter)

	// 启动 nix-store --dump 进程
	dumpCmd := exec.Command("nix-store", "--dump", storePath)
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

// GenerateNarInfo 组装标准的 .narinfo 元数据文本
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
