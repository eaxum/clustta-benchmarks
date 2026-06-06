# Temp Results — Windows C: Full-Depth Run (git-lfs + clustta)

**Date:** 2026-06-06
**Host:** Local Windows — Intel i7-14700F, 16 GB, C: = Samsung PM9A1 NVMe SSD
**Source:** `E:\spring-svn_repo\spring` (Blender Studio *Spring*, 3458 commit groups)
**Command:** `benchmark --systems git-lfs,clustta --discard-upstream --prune-local-lfs`

## Outcome

The run **crashed at commit 1656/3458** — C: filled to 0 bytes. We salvaged **1650
commits** of clean timing data for both systems before the failure, which is enough to
answer the question the run was designed to answer.

## Timing Results (1650 commits, Windows)

| System  | n    | Total    | Mean    | Median  | p95    | Max      |
|---------|------|----------|---------|---------|--------|----------|
| git-lfs | 1650 | 2526.9 s | 1.531 s | 1.390 s | 2.11 s | 155.06 s |
| clustta | 1650 | 1499.6 s | 0.909 s | 0.300 s | 3.71 s | 21.41 s  |

Windowed (degradation over history):

| System  | first-25% median | last-25% median | first-100 total |
|---------|------------------|-----------------|-----------------|
| git-lfs | 1.37 s           | 1.43 s          | 202.1 s         |
| clustta | 0.37 s           | 0.18 s          | 131.2 s         |

## Cross-OS Comparison (per-commit median)

| System  | Windows (C:) median | VM-Linux median | Windows penalty |
|---------|---------------------|-----------------|-----------------|
| git-lfs | 1.39 s              | 0.26 s          | **≈ 5.3×**      |
| clustta | 0.30 s              | 0.29 s          | **≈ 1.0×**      |

The first-100 git-lfs total (202.1 s) also matches the earlier standalone local-C:
100-commit run (189.4 s), confirming the measurement is consistent.

## Key Finding — the process-spawn tax, isolated

This run cleanly separates the two systems by *how* they commit:

- **git-lfs is process-spawn-bound.** Each commit shells out to ~7–10 `git` /
  `git-lfs` child processes. On Windows process creation is ~22× more expensive than
  on Linux, so git-lfs pays a **~5.3× per-commit median penalty** on Windows.
- **clustta is in-process.** Chunking + SQLite writes happen inside the Go binary with
  **no subprocess spawn**, so its median is **essentially identical across OSes
  (0.30 s vs 0.29 s)** — the Windows tax simply doesn't apply.

This is the decisive confirmation: the local-vs-VM gap is driven by **OS process-spawn
cost on the git toolchain**, not by disk (the fsync probe already showed local NVMe and
Azure NVMe are a tie at ~2 ms) and not by the machine. A system that avoids subprocess
spawning (clustta) is effectively OS-neutral on commit latency.

## Why it ran out of disk (estimate miss)

The ~165 GB ceiling I projected for git-lfs assumed `--prune-local-lfs` would bound
`.git/lfs` to the HEAD working set. It did **not**:

- `git lfs prune --force` ran each commit but reported `1746 local objects, 1746
  retained` — it **retained everything**. With near-`now` commit dates and the reflog
  holding recent commits, prune considered all LFS versions "recent/reachable", so
  `.git/lfs` accumulated full-size versions of every asset and grew unbounded.
- A second unaccounted consumer: the **SVN extraction working copy**
  (`svn_source_wc`) lives on C: and carries its own `.svn` pristine store, which also
  grows as it walks revisions.

Together these blew past the projection and filled C: at commit ~1656.

## Status of levers

- `--discard-upstream` — **worked as intended**; the bare git-lfs upstream was never
  populated (push skipped, ServerSize reported 0).
- `--prune-local-lfs` — **ineffective in practice** for this workload (prune retains all
  objects due to recent-date / reflog reachability). Needs a stronger approach
  (e.g. expire reflog + `--recent` overrides, or shallow/squash history) to actually
  bound `.git/lfs`.

## Salvaged / authoritative numbers to keep

- Windows per-commit medians: **git-lfs 1.39 s, clustta 0.30 s** (1650 commits).
- Process-spawn tax: **git-lfs ≈ 5.3× slower on Windows; clustta ≈ OS-neutral.**
- Full-depth server/storage figures remain the VM-Linux runs (git-lfs 601 GB un-pruned,
  clustta 168 GB deduplicated, SVN 248 GB).
