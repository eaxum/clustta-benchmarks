package replay

import (
	"clustta-benchmarks/internal/extract"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// GitReplayer benchmarks vanilla Git.
type GitReplayer struct {
	workDir string
}

func NewGitReplayer() *GitReplayer {
	return &GitReplayer{}
}

func (g *GitReplayer) Name() string {
	return "Git"
}

func (g *GitReplayer) Init(workDir string) error {
	g.workDir = workDir
	os.MkdirAll(workDir, 0755)

	if err := g.git("init"); err != nil {
		return fmt.Errorf("git init: %w", err)
	}
	if err := g.git("config", "user.email", "benchmark@clustta.com"); err != nil {
		return err
	}
	if err := g.git("config", "user.name", "Benchmark"); err != nil {
		return err
	}
	// Avoid OOM on large binary packing.
	if err := g.git("config", "pack.windowMemory", "512m"); err != nil {
		return err
	}
	return nil
}

func (g *GitReplayer) ReplayCommit(group extract.CommitGroup) (CommitMetrics, error) {
	var modifiedSize int64
	for _, f := range group.Files {
		destPath := filepath.Join(g.workDir, f.RelPath)
		os.MkdirAll(filepath.Dir(destPath), 0755)
		if err := copyFile(f.TempPath, destPath); err != nil {
			return CommitMetrics{}, fmt.Errorf("copy %s: %w", f.RelPath, err)
		}
		modifiedSize += f.FileSize
	}

	// Timed window (Option A): the local durable commit = `git add` + `git commit`.
	// Vanilla Git has no remote here, so this is the full distributed commit cost.
	start := time.Now()
	if err := g.git("add", "."); err != nil {
		return CommitMetrics{}, fmt.Errorf("git add: %w", err)
	}

	msg := fmt.Sprintf("commit %d", group.Index)
	if err := g.git("commit", "-m", msg, "--allow-empty"); err != nil {
		return CommitMetrics{}, fmt.Errorf("git commit: %w", err)
	}
	commitTime := time.Since(start).Seconds()

	totalSize := dirSizeMB(g.workDir)
	gitSize := dirSizeMB(filepath.Join(g.workDir, ".git"))

	return CommitMetrics{
		CommitNr:       group.Index,
		LocalSizeMB:    totalSize,
		MetadataSizeMB: gitSize,
		ModifiedFileMB: modifiedSize / (1024 * 1024),
		CommitTimeSec:  commitTime,
	}, nil
}

func (g *GitReplayer) Cleanup() error {
	return nil
}

// git runs a git command in the work dir.
func (g *GitReplayer) git(args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = g.workDir
	cmd.Stdout = nil
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
