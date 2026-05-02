package report

import (
	"clustta-benchmarks/internal/replay"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// WriteCSV writes a CSV for one system's results.
func WriteCSV(outputDir string, systemName string, metrics []replay.CommitMetrics) error {
	os.MkdirAll(outputDir, 0755)

	fileName := fmt.Sprintf("test_%s.csv", sanitize(systemName))
	filePath := filepath.Join(outputDir, fileName)

	f, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	// Header.
	w.Write([]string{
		"Commit nr",
		"Local checkout size",
		"Metadata size",
		"Server size",
		"Total size of modified files",
		"Commit time",
		"Sum of all added files",
		"Sum commit time",
	})

	// Data rows.
	for _, m := range metrics {
		w.Write([]string{
			strconv.Itoa(m.CommitNr),
			strconv.FormatInt(m.LocalSizeMB, 10),
			strconv.FormatInt(m.MetadataSizeMB, 10),
			strconv.FormatInt(m.ServerSizeMB, 10),
			strconv.FormatInt(m.ModifiedFileMB, 10),
			strconv.FormatFloat(m.CommitTimeSec, 'f', 2, 64),
			strconv.FormatInt(m.CumFileSizeMB, 10),
			strconv.FormatFloat(m.CumCommitTimeSec, 'f', 2, 64),
		})
	}

	return nil
}

// WriteGnuplotScript generates per-system and summary gnuplot charts.
func WriteGnuplotScript(outputDir string, systems []string) error {
	filePath := filepath.Join(outputDir, "plot_benchmark.gnuplot")

	f, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	// Shared dark-theme preamble matching Blender Studio's palette.
	preamble := `# Clustta Benchmark Visualization
# Auto-generated - run with: gnuplot plot_benchmark.gnuplot
#
# Mirrors the style used in the Blender Studio VCS benchmark:
# https://studio.blender.org/blog/svn-vs-git-lfs/

# Dark theme colours
set title textcolor rgb "#f0f0f0"
set ylabel textcolor rgb "#f0f0f0"
set y2label textcolor rgb "#f0f0f0"
set xlabel textcolor rgb "#f0f0f0"
set key textcolor rgb "#f0f0f0"
set xtics textcolor rgb "#f0f0f0"
set ytics textcolor rgb "#f0f0f0"
set border lc rgb "#f0f0f0"

set linetype 1 lc rgb "#ffa500"
set linetype 2 lc rgb "#44cc44"
set linetype 3 lc rgb "#c0c0c0"
set linetype 4 lc rgb "#0087ff"
set linetype 5 lc rgb "#b050e8"
set linetype 6 lc rgb "#fc1a70"
`
	fmt.Fprint(f, preamble)

	// ── Per-system multiplot (2×2) ──────────────────────────────────────
	nSystems := len(systems)
	cols := 2
	rows := (nSystems + 1) / 2

	fmt.Fprintln(f, `set term pngcairo size 1200,960 background "#2b2b2b" font "Sans,10"`)
	fmt.Fprintln(f, `set output 'benchmark_per_system.png'`)
	fmt.Fprintf(f, "set multiplot layout %d, %d columns\n\n", rows, cols)
	fmt.Fprintln(f, `set ylabel 'Size (GB)'`)
	fmt.Fprintln(f, `set y2label 'Time (Seconds)'`)
	fmt.Fprintln(f, `set xlabel 'Number of commits'`)
	fmt.Fprintln(f, `set grid`)
	fmt.Fprintln(f, `set datafile separator ','`)
	fmt.Fprintln(f, `set key top left`)
	fmt.Fprintln(f, `set style fill solid 0.35`)
	fmt.Fprintln(f, `set boxwidth 0.6 relative`)
	fmt.Fprintln(f, ``)

	// Column mapping (our CSV):
	//   1  Commit nr
	//   2  Local checkout size (MB)
	//   3  Metadata size (MB)
	//   4  Server size (MB)
	//   5  Total size of modified files (MB)
	//   6  Commit time (s)
	//   7  Sum of all added files (MB)
	//   8  Sum commit time (s)

	for _, sys := range systems {
		csvName := fmt.Sprintf("test_%s.csv", sanitize(sys))
		title := displayName(sys)
		fmt.Fprintf(f, "set title '%s'\n", title)

		// Sizes (÷1000 → GB), commit time bars scaled ÷100 to share axis,
		// and cumulative commit time in seconds.
		fmt.Fprintf(f, "plot './%s' using 1:($2/1e3) with lines title 'Local checkout (GB)', \\\n", csvName)
		fmt.Fprintf(f, "     '' using 1:($3/1e3) with lines title 'Metadata (GB)', \\\n")
		if sys == "svn" || sys == "perforce" {
			fmt.Fprintf(f, "     '' using 1:($4/1e3) with lines title 'Server (GB)', \\\n")
		}
		fmt.Fprintf(f, "     '' using 1:($7/1e3) with lines title 'Sum added files (GB)', \\\n")
		fmt.Fprintf(f, "     '' using 1:($5/1e2) with boxes title 'Modified / commit (×0.01 GB)', \\\n")
		fmt.Fprintf(f, "     '' using 1:($8/1e2) with lines title 'Sum commit time (×0.01 s)'\n\n")
	}
	fmt.Fprintln(f, "unset multiplot")

	// ── Summary comparison overlay ──────────────────────────────────────
	fmt.Fprintln(f, "")
	fmt.Fprintln(f, `# ── Summary comparison ──`)
	fmt.Fprintln(f, `set term pngcairo size 1200,500 background "#2b2b2b" font "Sans,10"`)
	fmt.Fprintln(f, `set output 'benchmark_summary.png'`)
	fmt.Fprintf(f, "set multiplot layout 1, 2\n\n")

	// Left panel: cumulative commit time.
	fmt.Fprintln(f, `set title 'Cumulative commit time'`)
	fmt.Fprintln(f, `set ylabel 'Time (s)'`)
	fmt.Fprintln(f, `set xlabel 'Number of commits'`)
	fmt.Fprintln(f, `unset y2label`)
	first := true
	for _, sys := range systems {
		csv := fmt.Sprintf("test_%s.csv", sanitize(sys))
		prefix := "     ''"
		if first {
			prefix = fmt.Sprintf("plot './%s'", csv)
			first = false
		} else {
			prefix = fmt.Sprintf("     './%s'", csv)
		}
		comma := `, \`
		if sys == systems[len(systems)-1] {
			comma = ""
		}
		fmt.Fprintf(f, "%s using 1:8 with lines lw 2 title '%s'%s\n", prefix, displayName(sys), comma)
	}

	// Right panel: metadata size.
	fmt.Fprintln(f, "")
	fmt.Fprintln(f, `set title 'Metadata / repository size'`)
	fmt.Fprintln(f, `set ylabel 'Size (GB)'`)
	first = true
	for _, sys := range systems {
		csv := fmt.Sprintf("test_%s.csv", sanitize(sys))
		prefix := "     ''"
		if first {
			prefix = fmt.Sprintf("plot './%s'", csv)
			first = false
		} else {
			prefix = fmt.Sprintf("     './%s'", csv)
		}
		comma := `, \`
		if sys == systems[len(systems)-1] {
			comma = ""
		}
		fmt.Fprintf(f, "%s using 1:($3/1e3) with lines lw 2 title '%s'%s\n", prefix, displayName(sys), comma)
	}

	fmt.Fprintln(f, "\nunset multiplot")
	return nil
}

// displayName maps a system key to its display title.
func displayName(sys string) string {
	switch sys {
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
	default:
		return sys
	}
}

func sanitize(name string) string {
	result := ""
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z':
			result += string(c)
		case c >= 'A' && c <= 'Z':
			result += string(c + 32) // lowercase
		case c >= '0' && c <= '9':
			result += string(c)
		case c == ' ' || c == '-':
			result += "_"
		}
	}
	return result
}
