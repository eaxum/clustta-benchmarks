package replay

import (
	"clustta-benchmarks/internal/extract"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// GitLFSReplayer benchmarks Git with LFS tracking.
type GitLFSReplayer struct {
	workDir     string
	upstreamDir string
}

func NewGitLFSReplayer() *GitLFSReplayer {
	return &GitLFSReplayer{}
}

func (g *GitLFSReplayer) Name() string {
	return "Git LFS"
}

func (g *GitLFSReplayer) Init(workDir string) error {
	g.workDir = workDir
	g.upstreamDir = workDir + "_upstream"

	os.MkdirAll(g.upstreamDir, 0755)
	cmd := exec.Command("git", "init", "--bare")
	cmd.Dir = g.upstreamDir
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git init --bare upstream: %w", err)
	}

	cloneCmd := exec.Command("git", "clone", g.upstreamDir, workDir)
	if err := cloneCmd.Run(); err != nil {
		return fmt.Errorf("git clone: %w", err)
	}

	if err := g.git("config", "user.email", "benchmark@clustta.com"); err != nil {
		return err
	}
	if err := g.git("config", "user.name", "Benchmark"); err != nil {
		return err
	}

	if err := g.git("lfs", "install"); err != nil {
		return fmt.Errorf("git lfs install: %w", err)
	}

	attribs := `*.blend filter=lfs diff=lfs merge=lfs -text
*.fbx filter=lfs diff=lfs merge=lfs -text
*.zip filter=lfs diff=lfs merge=lfs -text
*.mov filter=lfs diff=lfs merge=lfs -text
*.mp4 filter=lfs diff=lfs merge=lfs -text
*.png filter=lfs diff=lfs merge=lfs -text
*.jpg filter=lfs diff=lfs merge=lfs -text
*.pdf filter=lfs diff=lfs merge=lfs -text
*.exr filter=lfs diff=lfs merge=lfs -text
*.casc filter=lfs diff=lfs merge=lfs -text
`
	attribPath := filepath.Join(workDir, ".gitattributes")
	if err := os.WriteFile(attribPath, []byte(attribs), 0644); err != nil {
		return err
	}

	// Pre-auto-gc hook to prune LFS objects (same as Blender's approach).
	hooksDir := filepath.Join(workDir, ".git", "hooks")
	os.MkdirAll(hooksDir, 0755)
	hookContent := "#!/bin/sh\ngit lfs prune\nexit 0\n"
	hookPath := filepath.Join(hooksDir, "pre-auto-gc")
	os.WriteFile(hookPath, []byte(hookContent), 0755)

	if err := g.git("add", "."); err != nil {
		return err
	}
	if err := g.git("commit", "-m", "init: add .gitattributes for LFS"); err != nil {
		return err
	}
	if err := g.git("push", "-u", "origin", "HEAD"); err != nil {
		return fmt.Errorf("initial push: %w", err)
	}

	return nil
}

func (g *GitLFSReplayer) ReplayCommit(group extract.CommitGroup) (CommitMetrics, error) {
	var modifiedSize int64
	for _, f := range group.Files {
		destPath := filepath.Join(g.workDir, f.RelPath)
		os.MkdirAll(filepath.Dir(destPath), 0755)
		if err := copyFile(f.TempPath, destPath); err != nil {
			return CommitMetrics{}, fmt.Errorf("copy %s: %w", f.RelPath, err)
		}
		modifiedSize += f.FileSize
	}

	// Timed window (Option A): the local durable commit. For a distributed VCS
	// this is `git add` + `git commit` against the local repo. The subsequent
	// `git push` is the network/server sync and is intentionally NOT timed.
	start := time.Now()
	if err := g.git("add", "."); err != nil {
		return CommitMetrics{}, fmt.Errorf("git add: %w", err)
	}

	msg := fmt.Sprintf("commit %d", group.Index)
	if err := g.git("commit", "-m", msg, "--allow-empty"); err != nil {
		return CommitMetrics{}, fmt.Errorf("git commit: %w", err)
	}
	commitTime := time.Since(start).Seconds()

	// Push to the upstream is the distributed "sync" step, excluded from the
	// local-commit measurement. Kept (untimed) only so the upstream/server
	// size can still be reported on the storage axis.
	if err := g.git("push"); err != nil {
		return CommitMetrics{}, fmt.Errorf("git push: %w", err)
	}

	totalSize := dirSizeMB(g.workDir)
	gitSize := dirSizeMB(filepath.Join(g.workDir, ".git"))
	upstreamSize := dirSizeMB(g.upstreamDir)

	return CommitMetrics{
		CommitNr:       group.Index,
		LocalSizeMB:    totalSize,
		MetadataSizeMB: gitSize,
		ServerSizeMB:   upstreamSize,
		ModifiedFileMB: modifiedSize / (1024 * 1024),
		CommitTimeSec:  commitTime,
	}, nil
}

func (g *GitLFSReplayer) Cleanup() error {
	return nil
}

// git runs a git command in the work dir.
func (g *GitLFSReplayer) git(args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = g.workDir
	cmd.Stdout = nil
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
