#!/usr/bin/env bash
set -euo pipefail

spread_binary=${1:?usage: smoke/lib/run-smoke.sh SPREAD_BINARY SELECTOR}
selector=${2:?usage: smoke/lib/run-smoke.sh SPREAD_BINARY SELECTOR}

temp_dir=$(mktemp -d)
transcript="$temp_dir/spread-debug-socket.log"
remote_pwd="$temp_dir/remote-pwd"
spread_pid=""
socket_path=""

cleanup() {
    if [[ -n "$spread_pid" ]] && kill -0 "$spread_pid" 2>/dev/null; then
        if [[ -n "$socket_path" ]]; then
            "$spread_binary" continue "$socket_path" >/dev/null 2>&1 || kill -INT "$spread_pid" 2>/dev/null || true
        else
            kill -INT "$spread_pid" 2>/dev/null || true
        fi
        wait "$spread_pid" || true
    fi
    rm -f "$transcript" "$remote_pwd"
    rmdir "$temp_dir"
}
trap cleanup EXIT

: > "$transcript"
echo "Starting Spread debug-socket session for $selector"
"$spread_binary" -debug-socket "$selector" > >(tee "$transcript") 2>&1 &
spread_pid=$!

echo "Waiting for Spread to create the debug socket"
for ((i = 0; i < 600; i++)); do
    socket_path=$(sed -n 's/.* ready at "\([^"]*\)".*/\1/p' "$transcript" | tail -n 1)
    if [[ -n "$socket_path" && -S "$socket_path" ]]; then
        break
    fi
    if ! kill -0 "$spread_pid" 2>/dev/null; then
        echo "spread exited before creating the debug socket" >&2
        exit 1
    fi
    sleep 1
done

if [[ -z "$socket_path" || ! -S "$socket_path" ]]; then
    echo "timed out waiting for the debug socket" >&2
    exit 1
fi

task_path=${selector##*:}
expected_pwd="/root/spread/$task_path"
echo "Checking that the Spread session is in its task directory: $expected_pwd"
"$spread_binary" exec "$socket_path" -- pwd | tee "$remote_pwd"
if [[ $(<"$remote_pwd") != "$expected_pwd" ]]; then
    echo "unexpected remote working directory: $(<"$remote_pwd")" >&2
    exit 1
fi

echo "Continuing the Spread debug-socket session"
"$spread_binary" continue "$socket_path"
if wait "$spread_pid"; then
    status=0
else
    status=$?
fi
spread_pid=""
if ((status == 0)) || ! grep -q '^error: unsuccessful run$' "$transcript"; then
    echo "spread did not report the expected debug-socket task failure" >&2
    exit 1
fi
if [[ -e "$socket_path" ]]; then
    echo "Spread did not remove the debug socket when the session stopped" >&2
    exit 1
fi
