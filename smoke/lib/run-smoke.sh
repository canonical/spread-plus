#!/usr/bin/env bash
set -euo pipefail

spread_binary=${1:?usage: smoke/lib/run-smoke.sh SPREAD_BINARY SELECTOR}
selector=${2:?usage: smoke/lib/run-smoke.sh SPREAD_BINARY SELECTOR}
temp_dir=$(mktemp -d)
transcript="$temp_dir/spread-shell.log"
input_fifo="$temp_dir/shell-input"
os_release="$temp_dir/os-release"
shell_pid=""
input_open=0

# cleanup exits any still-open Spread shell and removes the helper's temporary files.
cleanup() {
    if [[ -n "$shell_pid" ]] && kill -0 "$shell_pid" 2>/dev/null && ((input_open)); then
        printf 'exit\r' >&3 || true
    fi
    if ((input_open)); then
        exec 3>&- || true
    fi
    if [[ -n "$shell_pid" ]]; then
        wait "$shell_pid" || true
    fi
    rm -f "$transcript" "$input_fifo" "$os_release"
    rmdir "$temp_dir"
}
trap cleanup EXIT

mkfifo "$input_fifo"
: > "$transcript"
printf -v spread_command '%q ' "$spread_binary" -shell -attach "$selector"
echo "Starting Spread shell for $selector"
# script provides the PTY Spread needs; the FIFO lets the test exit its shell later.
script --quiet --return --flush --command "$spread_command" "$transcript" < "$input_fifo" &
shell_pid=$!
exec 3> "$input_fifo"
input_open=1

# Wait until Spread reports the worker-specific socket and it exists locally.
echo "Waiting for Spread to create the attach socket"
socket=""
for ((i = 0; i < 600; i++)); do
    socket=$(sed -nE 's/.*spread attach ([^[:space:]]+) --.*/\1/p' "$transcript" | tail -n 1)
    if [[ -n "$socket" && -S "$socket" ]]; then
        break
    fi
    if ! kill -0 "$shell_pid" 2>/dev/null; then
        cat "$transcript"
        echo "spread exited before creating the attach socket" >&2
        exit 1
    fi
    sleep 1
done

if [[ -z "$socket" || ! -S "$socket" ]]; then
    cat "$transcript"
    echo "timed out waiting for the attach socket" >&2
    exit 1
fi

echo "Running cat /etc/os-release through the attach socket: $socket"
"$spread_binary" attach "$socket" -- 'cat /etc/os-release' | tee "$os_release"
grep -q '^ID=ubuntu$' "$os_release"

# Exit the remote shell so Spread can close the socket and finish the run.
echo "Closing the Spread shell"
printf 'exit\r' >&3
exec 3>&-
input_open=0
wait "$shell_pid"
shell_pid=""
