package replay

import (
	"clustta-benchmarks/internal/extract"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const p4Port = "1667"

// p4Exe and p4dExe locate the Perforce client/server binaries. They default to
// the standard Windows install paths on Windows and to bare names resolved on
// PATH elsewhere (Linux/macOS). Override with the P4_EXE / P4D_EXE env vars.
var (
	p4Exe  = resolveP4Bin("P4_EXE", `C:\Program Files\Perforce\p4.exe`, "p4")
	p4dExe = resolveP4Bin("P4D_EXE", `C:\Program Files\Perforce\Server\p4d.exe`, "p4d")
)

// resolveP4Bin picks the binary path: env override first, then the
// OS-appropriate default (Windows install path vs. bare name on PATH).
func resolveP4Bin(env, windowsPath, unixName string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	if runtime.GOOS == "windows" {
		return windowsPath
	}
	return unixName
}

// PerforceReplayer benchmarks Perforce (Helix Core).
type PerforceReplayer struct {
	workDir    string
	serverRoot string
	p4dCmd     *exec.Cmd
	user       string
	client     string
	env        []string
}

// NewPerforceReplayer creates a replayer with the given username.
func NewPerforceReplayer(user string) *PerforceReplayer {
	return &PerforceReplayer{
		user:   user,
		client: "benchmark-ws",
	}
}

func (p *PerforceReplayer) Name() string {
	return "Perforce"
}

// Init starts a local p4d and creates a workspace.
func (p *PerforceReplayer) Init(workDir string) error {
	p.workDir = workDir
	p.serverRoot = workDir + "_upstream"

	os.MkdirAll(p.serverRoot, 0755)
	os.MkdirAll(p.workDir, 0755)

	absServer, _ := filepath.Abs(p.serverRoot)

	// Fully isolate this server/client from any system-wide Perforce config.
	// The earlier configure-p4d.sh run persists credentials (P4PORT, P4USER,
	// P4PASSWD, tickets) in ~/.p4enviro and ~/.p4tickets; stripping process env
	// vars is not enough because p4 still reads those files, then sends the
	// *system* server's password to our fresh server -> "P4PASSWD invalid or
	// unset". Pointing P4ENVIRO/P4TICKETS/P4TRUST at throwaway paths under the
	// server root, plus explicit P4PORT/P4USER, guarantees a clean slate.
	p.env = isolatedP4Env(absServer, p.user)

	// Defensively stop any p4d left over from a previous (possibly failed) run
	// that may still be holding port 1667, so our bind doesn't fail.
	stop := exec.Command(p4Exe, "-p", "localhost:"+p4Port, "admin", "stop")
	stop.Env = p.env
	_ = stop.Run()
	time.Sleep(500 * time.Millisecond)

	p.p4dCmd = exec.Command(p4dExe, "-r", absServer, "-p", "localhost:"+p4Port)
	p.p4dCmd.Stdout = nil
	p.p4dCmd.Stderr = os.Stderr
	p.p4dCmd.Env = p.env
	if err := p.p4dCmd.Start(); err != nil {
		return fmt.Errorf("p4d start: %w", err)
	}
	time.Sleep(2 * time.Second)

	// On a brand-new server the first connection is auto-granted super, so we
	// can disable password security and enable user auto-creation. Non-fatal:
	// if the server already defaults to security=0 these are harmless no-ops.
	p.p4run("configure", "set", "security=0")
	p.p4run("configure", "set", "dm.user.noautocreate=0")

	absWork, _ := filepath.Abs(p.workDir)
	spec := fmt.Sprintf(
		"Client: %s\nOwner: %s\nRoot: %s\nOptions: allwrite clobber\nView:\n\t//depot/... //%s/...\n",
		p.client, p.user, absWork, p.client)

	cmd := exec.Command(p4Exe, "-p", "localhost:"+p4Port, "-u", p.user, "-c", p.client, "client", "-i")
	cmd.Env = p.env
	cmd.Stdin = strings.NewReader(spec)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("p4 client -i: %w\n%s", err, string(out))
	}

	return nil
}

// isolatedP4Env returns an environment for p4/p4d that is fully isolated from
// any system-wide Perforce configuration. It strips all inherited P4* vars and
// pins the connection settings, redirecting per-user state files (enviro,
// tickets, trust) into the throwaway server root.
func isolatedP4Env(serverRoot, user string) []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+5)
	for _, kv := range env {
		if strings.HasPrefix(kv, "P4") {
			continue
		}
		out = append(out, kv)
	}
	out = append(out,
		"P4PORT=localhost:"+p4Port,
		"P4USER="+user,
		"P4ENVIRO="+filepath.Join(serverRoot, ".p4enviro"),
		"P4TICKETS="+filepath.Join(serverRoot, ".p4tickets"),
		"P4TRUST="+filepath.Join(serverRoot, ".p4trust"),
	)
	return out
}

// ReplayCommit reconciles and submits one changelist.
func (p *PerforceReplayer) ReplayCommit(group extract.CommitGroup) (CommitMetrics, error) {
	var modifiedSize int64
	for _, f := range group.Files {
		destPath := filepath.Join(p.workDir, f.RelPath)
		os.MkdirAll(filepath.Dir(destPath), 0755)
		if err := copyFile(f.TempPath, destPath); err != nil {
			return CommitMetrics{}, fmt.Errorf("copy %s: %w", f.RelPath, err)
		}
		modifiedSize += f.FileSize
	}

	start := time.Now()
	reconcileErr := p.p4run("reconcile")

	if reconcileErr == nil {
		msg := fmt.Sprintf("commit %d", group.Index)
		if err := p.p4run("submit", "-d", msg); err != nil {
			return CommitMetrics{}, fmt.Errorf("p4 submit: %w", err)
		}
	}
	commitTime := time.Since(start).Seconds()

	totalSize := dirSizeMB(p.workDir)
	serverSize := dirSizeMB(p.serverRoot)

	return CommitMetrics{
		CommitNr:       group.Index,
		LocalSizeMB:    totalSize,
		MetadataSizeMB: serverSize,
		ServerSizeMB:   serverSize,
		ModifiedFileMB: modifiedSize / (1024 * 1024),
		CommitTimeSec:  commitTime,
	}, nil
}

// Cleanup stops the p4d server.
func (p *PerforceReplayer) Cleanup() error {
	if p.p4dCmd != nil && p.p4dCmd.Process != nil {
		p.p4run("admin", "stop")
		p.p4dCmd.Wait()
	}
	return nil
}

// p4run runs a p4 command in the work dir.
func (p *PerforceReplayer) p4run(args ...string) error {
	full := append([]string{"-p", "localhost:" + p4Port, "-u", p.user, "-c", p.client}, args...)
	cmd := exec.Command(p4Exe, full...)
	cmd.Dir = p.workDir
	cmd.Stdout = nil
	cmd.Stderr = os.Stderr
	cmd.Env = p.env
	return cmd.Run()
}
