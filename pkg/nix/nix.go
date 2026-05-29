package nix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

type PathInfo struct {
	Path       string   `json:"path"`
	NarHash    string   `json:"narHash"`
	NarSize    int64    `json:"narSize"`
	References []string `json:"references"`
	Signatures []string `json:"signatures,omitempty"`
}

func GetPathHash(storePath string) string {
	base := filepath.Base(storePath)
	if len(base) < 32 {
		return ""
	}
	return base[:32]
}

func GetPathName(storePath string) string {
	base := filepath.Base(storePath)
	if len(base) <= 33 {
		return base
	}
	return base[33:]
}

// GetClosure 获取输入路径的完整闭包 (Context-Aware)
func GetClosure(ctx context.Context, paths []string) ([]string, error) {
	args := append([]string{"--query", "--requisites"}, paths...)
	cmd := exec.CommandContext(ctx, "nix-store", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	var closure []string
	lines := strings.Split(out.String(), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			closure = append(closure, line)
		}
	}
	return closure, nil
}

// GetPathInfo 使用 nix path-info 读取某个路径的元数据 (Context-Aware)
func GetPathInfo(ctx context.Context, storePath string) (*PathInfo, error) {
	cmd := exec.CommandContext(ctx, "nix", "path-info", "--json", storePath)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	var list []PathInfo
	if err := json.Unmarshal(out.Bytes(), &list); err == nil && len(list) > 0 {
		if list[0].Path == "" {
			list[0].Path = storePath
		}
		return &list[0], nil
	}

	var dict map[string]PathInfo
	if err := json.Unmarshal(out.Bytes(), &dict); err == nil {
		for path, info := range dict {
			info.Path = path
			return &info, nil
		}
	}

	return nil, fmt.Errorf("failed to parse nix path-info output: %s", out.String())
}

// BuildTarget 执行本地 `nix build` 命令获取其 JSON 形式的输出路径 (Context-Aware)
func BuildTarget(ctx context.Context, target string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "nix", "build", target, "--no-link", "--json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%v: %s", err, errOut.String())
	}
	return ParseJSONBuildOutputs(out.Bytes())
}

func ParseJSONBuildOutputs(data []byte) ([]string, error) {
	var buildOutputs []map[string]interface{}
	if err := json.Unmarshal(data, &buildOutputs); err != nil {
		return nil, err
	}
	var paths []string
	for _, out := range buildOutputs {
		if outs, ok := out["outputs"].(map[string]interface{}); ok {
			for _, pathVal := range outs {
				if pStr, ok := pathVal.(string); ok {
					paths = append(paths, pStr)
				}
			}
		}
	}
	return paths, nil
}
