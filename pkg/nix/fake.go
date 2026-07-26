package nix

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
)

// FakeRunner is a test double providing in-memory Nix store responses.
type FakeRunner struct {
	Closures   map[string][]string
	PathInfos  map[string]PathInfo
	BuildPaths []string
	EvalPath   string

	// ExportContent is the fake compressed NAR data written to temp files.
	// Defaults to "fake-compressed-nar-data" if empty.
	ExportContent string
}

func NewFakeRunner() *FakeRunner {
	return &FakeRunner{
		Closures:  make(map[string][]string),
		PathInfos: make(map[string]PathInfo),
	}
}

func (f *FakeRunner) GetClosure(_ context.Context, paths []string) ([]string, error) {
	var result []string
	for _, p := range paths {
		if c, ok := f.Closures[p]; ok {
			result = append(result, c...)
		} else {
			result = append(result, p)
		}
	}
	return result, nil
}

func (f *FakeRunner) GetPathInfos(_ context.Context, storePaths []string) (map[string]PathInfo, error) {
	res := make(map[string]PathInfo, len(storePaths))
	for _, p := range storePaths {
		if info, ok := f.PathInfos[p]; ok {
			res[p] = info
		} else {
			res[p] = PathInfo{
				Path:    p,
				NarHash: "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
				NarSize: 1024,
			}
		}
	}
	return res, nil
}

func (f *FakeRunner) ExportAndCompress(_ context.Context, _, _ string, _, _ int) (string, string, int64, error) {
	content := []byte(f.ExportContent)
	if len(content) == 0 {
		content = []byte("fake-compressed-nar-data")
	}

	tmp, err := os.CreateTemp("", "fake-nar-*.nar.zst")
	if err != nil {
		return "", "", 0, err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", "", 0, err
	}
	_ = tmp.Close()

	h := sha256.Sum256(content)
	return tmp.Name(), hex.EncodeToString(h[:]), int64(len(content)), nil
}

func (f *FakeRunner) BuildTarget(_ context.Context, _ string) ([]string, error) {
	return f.BuildPaths, nil
}

func (f *FakeRunner) EvalOutPath(_ context.Context, _ string) (string, error) {
	return f.EvalPath, nil
}
