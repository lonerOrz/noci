package nix

import "context"

// Runner defines the execution contract for interacting with the Nix toolchain.
// Implementations swap the real os/exec calls for test doubles.
type Runner interface {
	GetClosure(ctx context.Context, paths []string) ([]string, error)
	GetPathInfos(ctx context.Context, storePaths []string) (map[string]PathInfo, error)
	ExportAndCompress(ctx context.Context, storePath, comp string, concurrency, level int) (tempFile string, fileHash string, fileSize int64, err error)
	BuildTarget(ctx context.Context, target string) ([]string, error)
	EvalOutPath(ctx context.Context, target string) (string, error)
}

// ExecRunner is the production adapter that calls real Nix CLI commands.
type ExecRunner struct{}

func (e ExecRunner) GetClosure(ctx context.Context, paths []string) ([]string, error) {
	return GetClosure(ctx, paths)
}

func (e ExecRunner) GetPathInfos(ctx context.Context, storePaths []string) (map[string]PathInfo, error) {
	return GetPathInfos(ctx, storePaths)
}

func (e ExecRunner) ExportAndCompress(ctx context.Context, storePath, comp string, concurrency, level int) (string, string, int64, error) {
	return ExportAndCompress(ctx, storePath, comp, concurrency, level)
}

func (e ExecRunner) BuildTarget(ctx context.Context, target string) ([]string, error) {
	return BuildTarget(ctx, target)
}

func (e ExecRunner) EvalOutPath(ctx context.Context, target string) (string, error) {
	return EvalOutPath(ctx, target)
}

// DefaultRunner is the production Nix executor. Overridable for tests at package level.
var DefaultRunner Runner = ExecRunner{}
