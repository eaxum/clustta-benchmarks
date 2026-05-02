# Clustta VCS Benchmark

A replay-based benchmark comparing version-control systems on real-world creative project data. Measures **commit speed** and **storage efficiency** across Git, Git LFS, SVN, Perforce, and Clustta.

## Background

### What is Clustta?

[Clustta](https://clustta.com) is an open source version-control and collaboration platform purpose-built for creative work - think **GitHub meets Google Drive**, designed from the ground up for the kinds of large binary files (`.blend`, `.psd`, `.fbx`, `.mov`, etc.) that traditional VCS tools struggle with.

Whilst Git was built for text-based source code and SVN predates the modern cloud era, Clustta is a modern alternative that treats large, opaque binary assets as first-class citizens.

### Motivation

In 2023, Blender Studio published a benchmark comparing SVN, Git, Git LFS, and Mercurial for managing their film projects.

> **[Benchmarking Version Control Solutions for Creative Collaboration](https://studio.blender.org/blog/benchmarking-version-control-git-lfs-svn-mercurial/)**

Their conclusion: **Git LFS with pre-compressed `.blend` files** was the best available option - but still far from ideal for creative teams. That's exactly the trade-off Blender Studio found: LFS trades storage efficiency for commit speed.

This benchmark extends that approach by adding Clustta and Perforce to the comparison, using the same "replay every commit and measure" methodology on a real creative production project.

## Results

155 commits from a real Clustta creative project (~4.5 GB of `.blend` files) replayed into each system. Lower is better on both axes.

![Summary comparison](results/benchmark_summary.svg)

| System | Cumulative commit time | Repository size |
|--------|----------------------:|----------------:|
| **Clustta** | 30.0 s | 1,408 MB |
| **Git LFS** | 79.5 s | 5,549 MB |
| **Git** | 88.6 s | 2,788 MB |
| **SVN** | 125.5 s | 5,549 MB |
| **Perforce** | 224.4 s | 2,863 MB |

### Test environment

| Component | Detail |
|-----------|--------|
| CPU | Intel Core i7-14700F (20 cores / 28 threads, up to 5.4 GHz) |
| RAM | 16 GB DDR5 |
| Disk | Samsung PM9A1 1 TB NVMe SSD (~6,900 / 5,100 MB/s seq R/W) |
| OS | Windows 11 Pro (build 26200) |
| Git | 2.45.2 |
| SVN | 1.14.5 |
| Perforce | P4 2025.2/2907753 |
| Clustta | v0.4.33 |

## How it works

### Replay methodology

The benchmark follows the same basic approach Blender Studio used:

1. **Extract** a chronological timeline of commits/checkpoint groups from an existing project repo
2. **Reconstruct** the full files
3. **Replay** the same sequence of file changes identically into fresh Git, Git LFS, SVN, Perforce, and Clustta repositories
4. **Measure** per-commit: commit/add time (seconds) and cumulative metadata/repository size (MB)
5. **Output** CSV data files and gnuplot visualisation scripts

### What each replayer does

| System | Commit operation timed | Metadata measured |
|--------|----------------------|-------------------|
| **Git** | `git add .` (staging into packfiles) | `.git/` directory |
| **Git LFS** | `git add .` + `git commit` + `git push` to bare upstream | `.git/` + upstream bare repo |
| **SVN** | `svn commit` to local `svnadmin` repository | `.svn/` + upstream repo |
| **Perforce** | `p4 reconcile` + `p4 submit` to local `p4d` | Server root (`db.*` + depot) |
| **Clustta** | Process and store via Clustta pipeline | `.clst` database file |

## Prerequisites

- **Go 1.22+**
- **Git** with **Git LFS** (`git lfs install`)
- **SVN** (`svn`, `svnadmin`)
- **Perforce** (`p4`, `p4d`) - optional, only needed if benchmarking Perforce
- **gnuplot** (for chart generation - optional)
- A Clustta `.clst` project file as the data source

## Usage

```bash
# Full run: extract timeline + replay all 5 systems
go run ./cmd/benchmark \
  --source "/path/to/project.clst" \
  --output ./results \
  --systems git,git-lfs,svn,perforce,clustta

# Re-run specific systems without re-extracting (uses saved timeline.json)
go run ./cmd/benchmark \
  --source "/path/to/project.clst" \
  --output ./results \
  --systems svn,clustta \
  --skip-extract

# Generate SVG charts from results
cd results
gnuplot plot_benchmark.gnuplot
# Opens: benchmark_per_system.svg  (2x2 per-system detail)
#        benchmark_summary.svg     (side-by-side comparison overlay)
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--source` | *(required)* | Path to source `.clst` file |
| `--output` | `./results` | Output directory for repos, CSVs, and charts |
| `--systems` | `git,git-lfs,svn,perforce,clustta` | Comma-separated list of systems to benchmark |
| `--skip-extract` | `false` | Skip extraction phase, reuse previously staged files |

## References

- [Blender Studio: SVN vs Git LFS benchmark](https://studio.blender.org/blog/svn-vs-git-lfs/) - The original inspiration and methodology reference
- [Clustta](https://clustta.com) - Version control for creative work
