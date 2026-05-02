package replay

import (
	"clustta-benchmarks/internal/extract"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// SVNReplayer benchmarks Subversion.
type SVNReplayer struct {
	workDir     string
	upstreamDir string
}

func NewSVNReplayer() *SVNReplayer {
	return &SVNReplayer{}
}

func (s *SVNReplayer) Name() string {
	return "SVN"
}

func (s *SVNReplayer) Init(workDir string) error {
	s.workDir = workDir
	s.upstreamDir = workDir + "_upstream"

	os.MkdirAll(s.upstreamDir, 0755)
	absUpstream, _ := filepath.Abs(s.upstreamDir)

	createCmd := exec.Command("svnadmin", "create", absUpstream)
	if err := createCmd.Run(); err != nil {
		return fmt.Errorf("svnadmin create: %w", err)
	}

	repoURL := "file:///" + filepath.ToSlash(absUpstream)
	coCmd := exec.Command("svn", "co", repoURL, workDir)
	if err := coCmd.Run(); err != nil {
		return fmt.Errorf("svn co: %w", err)
	}

	return nil
}

func (s *SVNReplayer) ReplayCommit(group extract.CommitGroup) (CommitMetrics, error) {
	var modifiedSize int64
	for _, f := range group.Files {
		destPath := filepath.Join(s.workDir, f.RelPath)
		os.MkdirAll(filepath.Dir(destPath), 0755)
		if err := copyFile(f.TempPath, destPath); err != nil {
			return CommitMetrics{}, fmt.Errorf("copy %s: %w", f.RelPath, err)
		}
		modifiedSize += f.FileSize

		if f.Operation == "add" {
			if err := s.svn("add", "--parents", destPath); err != nil {
				return CommitMetrics{}, fmt.Errorf("svn add %s: %w", f.RelPath, err)
			}
		}
	}

	start := time.Now()
	msg := fmt.Sprintf("commit %d", group.Index)
	if err := s.svn("commit", "-m", msg); err != nil {
		return CommitMetrics{}, fmt.Errorf("svn commit: %w", err)
	}
	commitTime := time.Since(start).Seconds()

	totalSize := dirSizeMB(s.workDir)
	svnSize := dirSizeMB(filepath.Join(s.workDir, ".svn"))
	upstreamSize := dirSizeMB(s.upstreamDir)

	return CommitMetrics{
		CommitNr:       group.Index,
		LocalSizeMB:    totalSize,
		MetadataSizeMB: svnSize,
		ServerSizeMB:   upstreamSize,
		ModifiedFileMB: modifiedSize / (1024 * 1024),
		CommitTimeSec:  commitTime,
	}, nil
}

func (s *SVNReplayer) Cleanup() error {
	return nil
}

// svn runs an svn command in the work dir.
func (s *SVNReplayer) svn(args ...string) error {
	cmd := exec.Command("svn", args...)
	cmd.Dir = s.workDir
	cmd.Stdout = nil
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
