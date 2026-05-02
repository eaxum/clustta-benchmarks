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
	source := flag.String("source", "", "Path to the source .clst file")
	output := flag.String("output", "./results", "Output directory for results")
	systems := flag.String("systems", "git,git-lfs,svn,perforce,clustta", "Comma-separated list of systems to benchmark (git,git-lfs,svn,perforce,clustta)")
	skipExtract := flag.Bool("skip-extract", false, "Skip extraction if staging dir already exists")
	reportOnly := flag.Bool("report-only", false, "Regenerate gnuplot script and CSV list from existing data (no replay)")
	flag.Parse()

	absOutput, _ := filepath.Abs(*output)

	if *reportOnly {
		systemList := parseSystemList(*systems)
		var csvSystems []string
		for _, sysName := range systemList {
			displayName := systemDisplayName(sysName)
			csvSystems = append(csvSystems, displayName)
		}
		if err := report.WriteGnuplotScript(absOutput, csvSystems); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing gnuplot script: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Wrote plot_benchmark.gnuplot")
		return
	}

	if *source == "" {
		fmt.Fprintln(os.Stderr, "Usage: benchmark --source <path-to.clst> [--output <dir>] [--systems <list>]")
		os.Exit(1)
	}

	absSource, _ := filepath.Abs(*source)
	stagingDir := filepath.Join(absOutput, "staging")

	systemList := parseSystemList(*systems)
	fmt.Printf("Benchmark Configuration:\n")
	fmt.Printf("  Source:  %s\n", absSource)
	fmt.Printf("  Output:  %s\n", absOutput)
	fmt.Printf("  Systems: %v\n", systemList)
	fmt.Println()

	// ═══════════════════════════════════════════════════════════════
	// STEP 1: EXTRACT TIMELINE + RECONSTRUCT FILES
	// ═══════════════════════════════════════════════════════════════
	var groups []extract.CommitGroup
	var err error

	if *skipExtract {
		fmt.Println("Skipping extraction (--skip-extract), loading saved timeline...")
		timelinePath := filepath.Join(stagingDir, "timeline.json")
		groups, err = extract.LoadTimeline(timelinePath, stagingDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading timeline.json: %v\n", err)
			fmt.Fprintln(os.Stderr, "  (Run without --skip-extract first to generate it.)")
			os.Exit(1)
		}
		fmt.Printf("Loaded %d commit groups from timeline.json\n\n", len(groups))
	} else {
		fmt.Println("Step 1: Extracting timeline and reconstructing files...")
		startExtract := time.Now()

		os.MkdirAll(stagingDir, 0755)
		groups, err = extract.StageAll(absSource, stagingDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error during extraction: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nExtracted %d commit groups in %s\n\n", len(groups), time.Since(startExtract).Round(time.Second))
	}

	// ═══════════════════════════════════════════════════════════════
	// STEP 2: REPLAY INTO EACH SYSTEM
	// ═══════════════════════════════════════════════════════════════
	allResults := make(map[string][]replay.CommitMetrics)

	for _, sysName := range systemList {
		replayer := createReplayer(sysName)
		if replayer == nil {
			fmt.Fprintf(os.Stderr, "Unknown system: %s (skipping)\n", sysName)
			continue
		}

		fmt.Printf("Step 2: Replaying into %s...\n", replayer.Name())
		repoDir := filepath.Join(absOutput, sanitizeDir(sysName)+"_repo")

		os.RemoveAll(repoDir)
		os.RemoveAll(repoDir + "_upstream")

		if err := replayer.Init(repoDir); err != nil {
			fmt.Fprintf(os.Stderr, "  Error initializing %s: %v\n", replayer.Name(), err)
			continue
		}

		var metrics []replay.CommitMetrics
		var cumFileSize int64
		var cumCommitTime float64
		startReplay := time.Now()

		for _, group := range groups {
			m, err := replayer.ReplayCommit(group)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  Error at commit %d: %v\n", group.Index, err)

				m = replay.CommitMetrics{CommitNr: group.Index}
			}

			cumFileSize += m.ModifiedFileMB
			cumCommitTime += m.CommitTimeSec
			m.CumFileSizeMB = cumFileSize
			m.CumCommitTimeSec = cumCommitTime

			metrics = append(metrics, m)

			if group.Index%10 == 0 || group.Index == len(groups) {
				fmt.Printf("  [%s] Commit %d/%d - %.1fs cumulative, %d MB metadata\n",
					replayer.Name(), group.Index, len(groups), cumCommitTime, m.MetadataSizeMB)
			}
		}

		replayer.Cleanup()
		allResults[sysName] = metrics
		elapsed := time.Since(startReplay).Round(time.Second)
		fmt.Printf("  %s complete: %d commits in %s (total commit time: %.1fs)\n\n",
			replayer.Name(), len(metrics), elapsed, cumCommitTime)
	}

	// ═══════════════════════════════════════════════════════════════
	// STEP 3: WRITE REPORTS
	// ═══════════════════════════════════════════════════════════════
	fmt.Println("Step 3: Writing reports...")
	var csvSystems []string
	for _, sysName := range systemList {
		metrics, ok := allResults[sysName]
		if !ok {
			continue
		}
		displayName := systemDisplayName(sysName)
		if err := report.WriteCSV(absOutput, displayName, metrics); err != nil {
			fmt.Fprintf(os.Stderr, "  Error writing CSV for %s: %v\n", displayName, err)
			continue
		}
		csvSystems = append(csvSystems, displayName)
		fmt.Printf("  Wrote test_%s.csv\n", sanitizeDir(displayName))
	}

	if len(csvSystems) > 0 {
		if err := report.WriteGnuplotScript(absOutput, csvSystems); err != nil {
			fmt.Fprintf(os.Stderr, "  Error writing gnuplot script: %v\n", err)
		} else {
			fmt.Println("  Wrote plot_benchmark.gnuplot")
		}
	}

	fmt.Println("\nDone!")
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
