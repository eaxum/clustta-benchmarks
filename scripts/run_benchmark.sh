#!/usr/bin/env bash
#
# run_benchmark.sh - build and run the VCS benchmark on the Azure VM.
#
# Lives in the repo under scripts/. On the VM, clone/pull the repo (e.g. to
# ~/clustta-benchmarks) and run it directly: ./scripts/run_benchmark.sh
# By default it expects the SVN source and writes results alongside the repo:
#   <parent>/spring-svn_repo/spring   (source)
#   <parent>/results                  (output)
# Override any of REPO / BASE / SOURCE / OUTPUT via env.
#
set -euo pipefail

# Resolve paths relative to this script's location (works from any CWD).
HERE="$(cd "$(dirname "$0")" && pwd)"

# ---- Configuration (override via env or edit) ------------------------------
REPO="${REPO:-$(cd "$HERE/.." && pwd)}"                 # benchmark code repo (scripts/ lives inside it)
BASE="${BASE:-$(cd "$REPO/.." && pwd)}"                 # parent dir holding the source + results
SOURCE="${SOURCE:-$BASE/spring-svn_repo/spring}"       # SVN repo root (has db/, format, conf/)
OUTPUT="${OUTPUT:-$BASE/results}"                       # results output dir
LIMIT="${LIMIT:-0}"                                    # 0 = all commits

# Which systems to benchmark. Defaults split the load across two VMs:
#   ROLE=a  -> perforce,clustta   (this VM)
#   ROLE=b  -> svn,git-lfs        (the other VM)
# Override directly with SYSTEMS=... to run any custom subset.
ROLE="${ROLE:-}"
if [[ -z "${SYSTEMS:-}" ]]; then
    case "$ROLE" in
        a|A) SYSTEMS="perforce,clustta" ;;
        b|B) SYSTEMS="svn,git-lfs" ;;
        *)   SYSTEMS="clustta,svn,git-lfs" ;;
    esac
fi
# ----------------------------------------------------------------------------

export PATH="$PATH:/usr/local/go/bin"

# Does the run include a given system? Usage: has_system perforce
has_system() { [[ ",$SYSTEMS," == *",$1,"* ]]; }

echo "==> Preflight checks"
for tool in go gcc git; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "ERROR: required tool '$tool' not found on PATH" >&2
        exit 1
    fi
done
if has_system svn; then
    for tool in svn svnadmin; do
        if ! command -v "$tool" >/dev/null 2>&1; then
            echo "ERROR: '$tool' required for 'svn' system but not found on PATH" >&2
            exit 1
        fi
    done
fi
if has_system git-lfs; then
    if ! git lfs version >/dev/null 2>&1; then
        echo "ERROR: git-lfs not installed (run: sudo apt-get install -y git-lfs && git lfs install --system)" >&2
        exit 1
    fi
fi
if has_system perforce; then
    for tool in p4 p4d; do
        if ! command -v "$tool" >/dev/null 2>&1; then
            echo "ERROR: '$tool' required for 'perforce' system but not found on PATH" >&2
            echo "       Install Helix Core: sudo apt-get install -y helix-p4d" >&2
            echo "       (or set P4_EXE / P4D_EXE to the binary paths)" >&2
            exit 1
        fi
    done
fi

if [[ ! -d "$REPO" ]]; then
    echo "ERROR: benchmark repo not found at: $REPO" >&2
    echo "       Set REPO=/path/to/clustta-benchmarks or edit this script." >&2
    exit 1
fi
if [[ ! -d "$SOURCE" ]]; then
    echo "ERROR: source repo not found at: $SOURCE" >&2
    echo "       Extract spring-svn_repo.zip, or set SOURCE=/path/to/spring." >&2
    exit 1
fi

echo "==> Building benchmark binary"
cd "$REPO"
mkdir -p bin
CGO_ENABLED=1 go build -o bin/benchmark ./cmd/benchmark

echo "==> Preparing output directory"
mkdir -p "$OUTPUT"
df -h "$OUTPUT" | sed 's/^/    /'

LOG="$OUTPUT/bench.log"
echo "==> Launching benchmark (detached, logging to $LOG)"
echo "    Source:  $SOURCE"
echo "    Output:  $OUTPUT"
echo "    Systems: $SYSTEMS"
[[ "$LIMIT" != "0" ]] && echo "    Limit:   $LIMIT commits"

ARGS=(--source "$SOURCE" --source-type svn --systems "$SYSTEMS" --output "$OUTPUT")
[[ "$LIMIT" != "0" ]] && ARGS+=(--limit "$LIMIT")

nohup "$REPO/bin/benchmark" "${ARGS[@]}" > "$LOG" 2>&1 &
PID=$!
echo "$PID" > "$OUTPUT/bench.pid"

# Launch the ntfy progress watcher (optional; needs curl). NOTIFY=0 to skip.
if [[ "${NOTIFY:-1}" != "0" ]] && command -v curl >/dev/null 2>&1; then
    TOTAL="${TOTAL:-3458}" LABEL="${LABEL:-$SYSTEMS}" SYSTEMS="$SYSTEMS" \
        nohup "$HERE/bench_notify.sh" "$LOG" "$OUTPUT" > "$OUTPUT/notify.log" 2>&1 &
    echo "$!" > "$OUTPUT/notify.pid"
    echo "==> ntfy watcher started (topic in $HERE/.bench_topic.txt, see $OUTPUT/notify.log)"
fi

echo
echo "Started (PID $PID). Useful commands:"
echo "    tail -f $LOG          # follow progress"
echo "    kill \$(cat $OUTPUT/bench.pid)   # stop the run"
