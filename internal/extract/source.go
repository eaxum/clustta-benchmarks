package extract

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Source produces a chronological timeline of commit groups and materialises
// each group's files to disk so the replayers can consume them.
type Source interface {
	// BuildTimeline returns the ordered list of commit groups.
	BuildTimeline() ([]CommitGroup, error)
	// StageGroup materialises the files for one group, setting each
	// FileOp.TempPath (and FileSize) to point at the on-disk file.
	StageGroup(group *CommitGroup, stagingDir string) error
	// CleanGroup frees any disk used by a previously staged group.
	CleanGroup(stagingDir string, index int) error
	// Close releases the underlying resource (database handle, etc.).
	Close() error
}

// CleanGroup satisfies Source for the .clst backend.
func (s *StreamSource) CleanGroup(stagingDir string, index int) error {
	return CleanGroup(stagingDir, index)
}

// SvnSource replays history from a local Subversion repository by walking a
// single persistent working copy forward with incremental `svn update`.
type SvnSource struct {
	repoURL string
	workDir string
}

// OpenSvnSource checks out an empty working copy (revision 0) of repoURL at
// workDir. repoURL may be a URL or a local path (converted to a file:// URL).
func OpenSvnSource(repoURL string, workDir string) (*SvnSource, error) {
	url := ToSvnURL(repoURL)

	if err := os.RemoveAll(workDir); err != nil {
		return nil, fmt.Errorf("clean working copy: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(workDir), 0755); err != nil {
		return nil, fmt.Errorf("create working copy parent: %w", err)
	}

	cmd := exec.Command("svn", "checkout", "-r", "0", url, workDir)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("svn checkout -r 0 %s: %w", url, err)
	}

	return &SvnSource{repoURL: url, workDir: workDir}, nil
}

// ToSvnURL converts a local filesystem path into a file:// URL. Inputs that
// already look like a URL (contain "://") are returned unchanged.
func ToSvnURL(source string) string {
	if strings.Contains(source, "://") {
		return source
	}
	abs, err := filepath.Abs(source)
	if err != nil {
		abs = source
	}
	return "file:///" + filepath.ToSlash(abs)
}

func (s *SvnSource) Close() error { return nil }

// CleanGroup is a no-op: the working copy is the rolling state, so deleting a
// revision's files would force an expensive re-fetch on the next update.
func (s *SvnSource) CleanGroup(stagingDir string, index int) error { return nil }

// svnLogEntry / svnLogPath mirror the `svn log -v --xml` output.
type svnLog struct {
	Entries []svnLogEntry `xml:"logentry"`
}

type svnLogEntry struct {
	Revision int          `xml:"revision,attr"`
	Paths    []svnLogPath `xml:"paths>path"`
}

type svnLogPath struct {
	Action string `xml:"action,attr"`
	Kind   string `xml:"kind,attr"`
	Path   string `xml:",chardata"`
}

// BuildTimeline parses `svn log -v --xml -r 1:HEAD` into one CommitGroup per
// revision, keeping added/modified/replaced files and dropping deletes/dirs.
func (s *SvnSource) BuildTimeline() ([]CommitGroup, error) {
	cmd := exec.Command("svn", "log", "-v", "--xml", "-r", "1:HEAD", s.repoURL)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("svn log: %w", err)
	}

	var parsed svnLog
	if err := xml.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("parse svn log xml: %w", err)
	}

	seenAssets := make(map[string]bool)
	var groups []CommitGroup
	idx := 0

	for _, entry := range parsed.Entries {
		var files []FileOp
		for _, p := range entry.Paths {
			if p.Action == "D" {
				continue
			}
			if p.Kind == "dir" {
				continue
			}
			relPath := strings.TrimPrefix(strings.TrimSpace(p.Path), "/")
			if relPath == "" {
				continue
			}

			op := "modify"
			if !seenAssets[relPath] {
				op = "add"
				seenAssets[relPath] = true
			}

			files = append(files, FileOp{
				RelPath:   relPath,
				Operation: op,
			})
		}

		if len(files) == 0 {
			continue
		}

		idx++
		groups = append(groups, CommitGroup{
			Index:   idx,
			GroupId: strconv.Itoa(entry.Revision),
			Files:   files,
		})
	}

	if len(groups) == 0 {
		return nil, fmt.Errorf("no file-changing revisions found in %s", s.repoURL)
	}
	fmt.Printf("  SVN history: %d revisions with file changes\n", len(groups))
	return groups, nil
}

// StageGroup rolls the working copy to the group's revision via incremental
// `svn update`, then resolves each FileOp to its working-copy path.
func (s *SvnSource) StageGroup(group *CommitGroup, stagingDir string) error {
	rev := group.GroupId
	cmd := exec.Command("svn", "update", "--quiet", "-r", rev, s.workDir)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("svn update -r %s: %w", rev, err)
	}

	kept := group.Files[:0]
	for _, f := range group.Files {
		wcPath := filepath.Join(s.workDir, filepath.FromSlash(f.RelPath))
		fi, err := os.Stat(wcPath)
		if err != nil || fi.IsDir() {
			continue
		}
		f.TempPath = wcPath
		f.FileSize = fi.Size()
		kept = append(kept, f)
	}
	group.Files = kept
	return nil
}
