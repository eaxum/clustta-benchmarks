#!/usr/bin/env bash
#
# bench_notify.sh - push benchmark progress to ntfy.sh and respond to
# read-only commands (status, diskcheck) sent to the topic.
#
# Usage: ./bench_notify.sh <logfile> <output-dir> [topic]
# If no topic is given, a random one is generated and saved next to this script.
#
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
LOG="${1:?usage: bench_notify.sh <logfile> <output-dir> [topic]}"
OUTPUT="${2:?usage: bench_notify.sh <logfile> <output-dir> [topic]}"
TOPIC="${3:-}"
TOTAL="${TOTAL:-3458}"
INTERVAL="${INTERVAL:-600}"   # progress push interval (seconds)

# Label distinguishes notifications when running on multiple VMs. Defaults to
# the systems being benchmarked (set by run_benchmark.sh) or the hostname.
LABEL="${LABEL:-${SYSTEMS:-$(hostname)}}"

TOPIC_FILE="$HERE/.bench_topic.txt"
if [[ -z "$TOPIC" ]]; then
    if [[ -f "$TOPIC_FILE" ]]; then
        TOPIC="$(cat "$TOPIC_FILE")"
    else
        TOPIC="clustta-bench-$(tr -dc 'a-z0-9' </dev/urandom | head -c 10)"
        echo "$TOPIC" > "$TOPIC_FILE"
    fi
fi
NTFY="https://ntfy.sh/$TOPIC"

send() {  # send <title> <tags> <priority> <body>
    curl -s \
        -H "Title: [$LABEL] $1" \
        -H "Tags: $2" \
        -H "Priority: $3" \
        -d "$4" "$NTFY" >/dev/null 2>&1 || true
}

progress_body() {
    local line commit pct used free
    line="$(grep -oE 'Commit [0-9]+/[0-9]+' "$LOG" 2>/dev/null | tail -1)"
    commit="$(echo "$line" | grep -oE '[0-9]+' | head -1)"
    commit="${commit:-0}"
    pct=$(awk "BEGIN{ if ($TOTAL>0) printf \"%.1f\", $commit/$TOTAL*100; else print 0 }")
    used="$(du -sh "$OUTPUT" 2>/dev/null | cut -f1)"
    free="$(df -h "$OUTPUT" 2>/dev/null | awk 'NR==2{print $4}')"
    printf 'Commit %s/%s (%s%%)\nResults: %s\nDisk free: %s' \
        "$commit" "$TOTAL" "$pct" "${used:-?}" "${free:-?}"
}

diskcheck_body() {
    { df -h "$OUTPUT" 2>/dev/null | awk 'NR==1||NR==2'; echo "--- subdirs ---"; \
      du -sh "$OUTPUT"/*/ 2>/dev/null; } | head -20
}

# Free space in GB for the low-disk alert.
free_gb() { df -BG "$OUTPUT" 2>/dev/null | awk 'NR==2{gsub("G","",$4); print $4}'; }

bench_running() { pgrep -x benchmark >/dev/null 2>&1; }

echo "ntfy topic: $TOPIC"
echo "URL:        $NTFY"
send "Benchmark watcher started" "rocket" "default" "Tracking on $(hostname). Systems: $LABEL. Topic: $TOPIC"

# Wait up to 60s for the benchmark process to appear before monitoring,
# so we don't declare "finished" before it has even started.
for _ in $(seq 1 12); do
    bench_running && break
    sleep 5
done

last_reported=-1
last_progress=0
since=$(date +%s)
while true; do
    # --- handle inbound commands (read-only) every loop (~5s latency) ---
    msgs="$(curl -s "https://ntfy.sh/$TOPIC/json?poll=1&since=${since}" 2>/dev/null)"
    if [[ -n "$msgs" ]]; then
        while IFS= read -r m; do
            [[ -z "$m" ]] && continue
            ev="$(echo "$m" | grep -oE '"event":"[^"]*"' | cut -d'"' -f4)"
            [[ "$ev" != "message" ]] && continue
            cmd="$(echo "$m" | grep -oE '"message":"[^"]*"' | cut -d'"' -f4 | tr '[:upper:]' '[:lower:]' | xargs)"
            case "$cmd" in
                status)    send "Status" "bar_chart" "default" "$(progress_body)";;
                diskcheck) send "Disk check" "floppy_disk" "default" "$(diskcheck_body)";;
            esac
        done <<< "$msgs"
    fi
    since=$(date +%s)

    # --- finished? send final summary and exit ---
    if ! bench_running; then
        send "Benchmark FINISHED" "checkered_flag" "high" "$(progress_body)"
        break
    fi

    # --- low-disk alert ---
    fg="$(free_gb)"
    if [[ -n "$fg" && "$fg" -lt 30 ]]; then
        send "LOW DISK WARNING" "warning" "urgent" "Only ${fg}G free!
$(progress_body)"
    fi

    # --- periodic progress (every INTERVAL, only when commit advanced) ---
    now=$(date +%s)
    if (( now - last_progress >= INTERVAL )); then
        cur="$(grep -oE 'Commit [0-9]+/' "$LOG" 2>/dev/null | tail -1 | grep -oE '[0-9]+')"
        cur="${cur:-0}"
        if [[ "$cur" != "$last_reported" ]]; then
            send "Benchmark progress" "bar_chart" "default" "$(progress_body)"
            last_reported="$cur"
        fi
        last_progress="$now"
    fi

    sleep 5
done
