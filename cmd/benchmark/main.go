package main

import (
	"clustta-benchmarks/internal/extract"
	"clustta-benchmarks/internal/replay"
	"clustta-benchmarks/internal/report"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	source := flag.String("source", "", "Path to the source: a .clst file or a local SVN repo/URL")
	sourceType := flag.String("source-type", "auto", "Source backend: clst, svn, or auto (detect from --source)")
	output := flag.String("output", "./results", "Output directory for results")
	systems := flag.String("systems", "git,git-lfs,svn,perforce,clustta", "Comma-separated list of systems to benchmark")
	limit := flag.Int("limit", 0, "Max commit groups to process (0 = all)")
	skipExtract := flag.Bool("skip-extract", false, "Use pre-staged files instead of streaming")
	reportOnly := flag.Bool("report-only", false, "Regenerate gnuplot script from existing CSV data")
	flag.Parse()

	absOutput, _ := filepath.Abs(*output)

	if *reportOnly {
		systemList := parseSystemList(*systems)
		var csvSystems []string
		for _, sysName := range systemList {
			csvSystems = append(csvSystems, systemDisplayName(sysName))
		}
		if err := report.WriteGnuplotScript(absOutput, csvSystems); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing gnuplot script: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Wrote plot_benchmark.gnuplot")
		return
	}

	if *source == "" {
		fmt.Fprintln(os.Stderr, "Usage: benchmark --source <path-to.clst> [--output <dir>] [--systems <list>] [--limit <n>]")
		os.Exit(1)
	}

	absSource, _ := filepath.Abs(*source)
	stagingDir := filepath.Join(absOutput, "staging")
	systemList := parseSystemList(*systems)

	sourceKind := resolveSourceKind(*sourceType, *source)

	fmt.Printf("Benchmark Configuration:\n")
	fmt.Printf("  Source:  %s\n", absSource)
	fmt.Printf("  Type:    %s\n", sourceKind)
	fmt.Printf("  Output:  %s\n", absOutput)
	fmt.Printf("  Systems: %v\n", systemList)
	if *limit > 0 {
		fmt.Printf("  Limit:   %d commits\n", *limit)
	}
	fmt.Println()

	// Build or load timeline.
	var groups []extract.CommitGroup
	var src extract.Source

	if *skipExtract {
		fmt.Println("Loading saved timeline (--skip-extract)...")
		timelinePath := filepath.Join(stagingDir, "timeline.json")
		var err error
		groups, err = extract.LoadTimeline(timelinePath, stagingDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading timeline: %v\n", err)
			os.Exit(1)
		}
	} else {
		var err error
		switch sourceKind {
		case "svn":
			fmt.Println("Building timeline from SVN repository...")
			workCopy := filepath.Join(absOutput, "svn_source_wc")
			src, err = extract.OpenSvnSource(*source, workCopy)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error opening SVN source: %v\n", err)
				os.Exit(1)
			}
		default:
			fmt.Println("Building timeline from .clst...")
			src, err = extract.OpenStream(absSource)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
				os.Exit(1)
			}
		}
		defer src.Close()
		groups, err = src.BuildTimeline()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error building timeline: %v\n", err)
			os.Exit(1)
		}
	}

	if *limit > 0 && *limit < len(groups) {
		groups = groups[:*limit]
	}
	fmt.Printf("Processing %d commit groups\n\n", len(groups))

	// Init all replayers.
	type runner struct {
		name    string
		sys     string
		r       replay.Replayer
		metrics []replay.CommitMetrics
		cumFile int64
		cumTime float64
	}
	var runners []runner

	for _, sysName := range systemList {
		r := createReplayer(sysName)
		if r == nil {
			fmt.Fprintf(os.Stderr, "Unknown system: %s (skipping)\n", sysName)
			continue
		}
		repoDir := filepath.Join(absOutput, sanitizeDir(sysName)+"_repo")
		os.RemoveAll(repoDir)
		os.RemoveAll(repoDir + "_upstream")
		if err := r.Init(repoDir); err != nil {
			fmt.Fprintf(os.Stderr, "Error initializing %s: %v\n", r.Name(), err)
			continue
		}
		runners = append(runners, runner{name: r.Name(), sys: sysName, r: r})
	}

	if len(runners) == 0 {
		fmt.Fprintln(os.Stderr, "No systems initialized")
		os.Exit(1)
	}

	// Replay commits. In streaming mode: extract -> replay all systems -> clean per commit.
	os.MkdirAll(stagingDir, 0755)
	startAll := time.Now()

	for i := range groups {
		group := &groups[i]

		if src != nil {
			if err := src.StageGroup(group, stagingDir); err != nil {
				fmt.Fprintf(os.Stderr, "  Error staging commit %d: %v\n", group.Index, err)
				continue
			}
		}

		for j := range runners {
			m, err := runners[j].r.ReplayCommit(*group)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  [%s] Error at commit %d: %v\n", runners[j].name, group.Index, err)
				m = replay.CommitMetrics{CommitNr: group.Index}
			}
			runners[j].cumFile += m.ModifiedFileMB
			runners[j].cumTime += m.CommitTimeSec
			m.CumFileSizeMB = runners[j].cumFile
			m.CumCommitTimeSec = runners[j].cumTime
			runners[j].metrics = append(runners[j].metrics, m)
		}

		if src != nil {
			src.CleanGroup(stagingDir, group.Index)
		}

		// Save CSVs every 10 commits for crash resilience.
		if group.Index%10 == 0 || i == len(groups)-1 {
			for j := range runners {
				displayName := systemDisplayName(runners[j].sys)
				report.WriteCSV(absOutput, displayName, runners[j].metrics)
			}
		}

		if group.Index%10 == 0 || group.Index == len(groups) {
			fmt.Printf("  Commit %d/%d (%s elapsed)\n", group.Index, len(groups), time.Since(startAll).Round(time.Second))
		}
	}

	for i := range runners {
		runners[i].r.Cleanup()
	}
	fmt.Printf("\nAll systems complete in %s\n\n", time.Since(startAll).Round(time.Second))

	// Write reports.
	fmt.Println("Writing reports...")
	var csvSystems []string
	for _, rn := range runners {
		displayName := systemDisplayName(rn.sys)
		if err := report.WriteCSV(absOutput, displayName, rn.metrics); err != nil {
			fmt.Fprintf(os.Stderr, "  Error writing CSV for %s: %v\n", displayName, err)
			continue
		}
		csvSystems = append(csvSystems, displayName)
		fmt.Printf("  Wrote test_%s.csv\n", sanitizeDir(displayName))
	}

	if len(csvSystems) > 0 {
		if err := report.WriteGnuplotScript(absOutput, csvSystems); err != nil {
			fmt.Fprintf(os.Stderr, "  Error writing gnuplot: %v\n", err)
		} else {
			fmt.Println("  Wrote plot_benchmark.gnuplot")
		}
	}

	fmt.Println("\nDone!")
}

// resolveSourceKind decides which source backend to use. An explicit
// --source-type wins; otherwise it is inferred from the --source path.
func resolveSourceKind(sourceType, source string) string {
	kind := strings.TrimSpace(strings.ToLower(sourceType))
	if kind != "" && kind != "auto" {
		return kind
	}
	if strings.HasSuffix(strings.ToLower(source), ".clst") {
		return "clst"
	}
	return "svn"
}

func parseSystemList(s string) []string {
	var result []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(strings.ToLower(part))
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func createReplayer(name string) replay.Replayer {
	switch name {
	case "git":
		return replay.NewGitReplayer()
	case "git-lfs":
		return replay.NewGitLFSReplayer()
	case "svn":
		return replay.NewSVNReplayer()
	case "perforce":
		return replay.NewPerforceReplayer("taiwo")
	case "clustta":
		return replay.NewClusttaReplayer()
	}
	return nil
}

func systemDisplayName(name string) string {
	switch name {
	case "git":
		return "Git"
	case "git-lfs":
		return "Git LFS"
	case "svn":
		return "SVN"
	case "perforce":
		return "Perforce"
	case "clustta":
		return "Clustta"
	}
	return name
}

func sanitizeDir(name string) string {
	result := ""
	for _, c := range strings.ToLower(name) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			result += string(c)
		case c == ' ', c == '-':
			result += "_"
		}
	}
	return result
}
