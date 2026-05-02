package replay

import (
	"clustta-benchmarks/internal/extract"
)

// CommitMetrics holds per-commit measurements.
type CommitMetrics struct {
	CommitNr         int
	LocalSizeMB      int64
	MetadataSizeMB   int64
	ServerSizeMB     int64
	ModifiedFileMB   int64
	CommitTimeSec    float64
	CumFileSizeMB    int64
	CumCommitTimeSec float64
}

// Replayer is the interface each VCS must implement.
type Replayer interface {
	Name() string
	Init(workDir string) error
	ReplayCommit(group extract.CommitGroup) (CommitMetrics, error)
	Cleanup() error
}
