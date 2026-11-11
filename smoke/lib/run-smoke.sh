#!/usr/bin/env bash
set -euo pipefail

spread_binary=${1:?usage: smoke/lib/run-smoke.sh SPREAD_BINARY SELECTOR}
selector=${2:?usage: smoke/lib/run-smoke.sh SPREAD_BINARY SELECTOR}
temp_dir=$(mktemp -d)
transcript="$temp_dir/spread-shell.log"
input_fifo="$temp_dir/shell-input"
remote_pwd="$temp_dir/remote-pwd"
shell_pid=""
input_open=0

if [[ -e .spread.socket ]]; then
    echo "stale .spread.socket exists before starting the Spread shell" >&2
    exit 1
fi

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
    rm -f "$transcript" "$input_fifo" "$remote_pwd"
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

# Wait for Spread to create the socket for the interactive shell.
echo "Waiting for Spread to create the exec socket"
for ((i = 0; i < 600; i++)); do
    if [[ -S .spread.socket ]]; then
        break
    fi
    if ! kill -0 "$shell_pid" 2>/dev/null; then
        cat "$transcript"
        echo "spread exited before creating the exec socket" >&2
        exit 1
    fi
    sleep 1
done

if [[ ! -S .spread.socket ]]; then
    cat "$transcript"
    echo "timed out waiting for the exec socket" >&2
    exit 1
fi

task_path=${selector##*:}
expected_pwd="/root/spread/$task_path"
echo "Checking that the Spread shell is in its task directory: $expected_pwd"
"$spread_binary" exec -- pwd | tee "$remote_pwd"
if [[ $(<"$remote_pwd") != "$expected_pwd" ]]; then
    echo "unexpected remote working directory: $(<"$remote_pwd")" >&2
    exit 1
fi

# Exit the remote shell so Spread can close the socket and finish the run.
echo "Closing the Spread shell"
printf 'exit\r' >&3
exec 3>&-
input_open=0
wait "$shell_pid"
shell_pid=""
if [[ -e .spread.socket ]]; then
    echo "Spread did not remove the exec socket when the shell closed" >&2
    exit 1
fi
