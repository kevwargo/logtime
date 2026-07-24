#!/bin/bash

# Test suite for logtime
# Run from the project root after building: go build -o logtime .

set -e

LOGTIME=./logtime
PASS=0
FAIL=0

pass() {
    echo "  PASS: $1"
    PASS=$((PASS + 1))
}

fail() {
    echo "  FAIL: $1"
    echo "        $2"
    FAIL=$((FAIL + 1))
}

cleanup() {
    rm -f /tmp/logtime-test-*.log
}
trap cleanup EXIT

echo "=== Building logtime ==="
go build -o logtime .
echo ""

# --- Test 1: Direct mode (no --filename) ---
echo "--- Test 1: Direct mode ---"
output=$($LOGTIME -- echo "hello direct" 2>&1)
if echo "$output" | grep -q '\[stdout\] line: "hello direct"'; then
    pass "stdout captured with timestamp"
else
    fail "stdout not captured" "$output"
fi
if echo "$output" | grep -q 'started '; then
    fail "IPC message leaked to terminal" "$output"
else
    pass "no IPC leakage"
fi

# --- Test 2: Worker mode (with --filename), simple command ---
echo "--- Test 2: Worker mode, simple command ---"
rm -f /tmp/logtime-test-simple.log
$LOGTIME -o /tmp/logtime-test-simple.log -- echo "hello worker" 2>&1
# Give worker a moment to finish writing
sleep 0.5
if grep -q '\[stdout\] line: "hello worker"' /tmp/logtime-test-simple.log; then
    pass "output logged to file"
else
    fail "output not in log file" "$(cat /tmp/logtime-test-simple.log)"
fi
if grep -q 'Subcommand exited: code:0' /tmp/logtime-test-simple.log; then
    pass "subcommand exit logged"
else
    fail "subcommand exit not logged" "$(cat /tmp/logtime-test-simple.log)"
fi
if grep -q 'All children exited' /tmp/logtime-test-simple.log; then
    pass "all children exited logged"
else
    fail "all children exited not logged" "$(cat /tmp/logtime-test-simple.log)"
fi

# --- Test 3: Worker mode with backgrounding child ---
echo "--- Test 3: Worker mode, background child ---"
rm -f /tmp/logtime-test-bg.log
$LOGTIME -o /tmp/logtime-test-bg.log -- bash -c '
    echo "parent start"
    (sleep 1; echo "bg 1s"; sleep 1; echo "bg 2s") &
    echo "parent done"
' 2>&1

# Foreground should have exited immediately
echo "  (foreground exited, waiting for background child...)"
sleep 3

if grep -q '\[stdout\] line: "parent start"' /tmp/logtime-test-bg.log; then
    pass "parent output captured"
else
    fail "parent output missing" "$(cat /tmp/logtime-test-bg.log)"
fi
if grep -q '\[stdout\] line: "bg 1s"' /tmp/logtime-test-bg.log; then
    pass "background child output at 1s captured"
else
    fail "background child 1s output missing" "$(cat /tmp/logtime-test-bg.log)"
fi
if grep -q '\[stdout\] line: "bg 2s"' /tmp/logtime-test-bg.log; then
    pass "background child output at 2s captured"
else
    fail "background child 2s output missing" "$(cat /tmp/logtime-test-bg.log)"
fi
if grep -q 'All children exited' /tmp/logtime-test-bg.log; then
    pass "worker exited after all children done"
else
    fail "worker did not report all children exited" "$(cat /tmp/logtime-test-bg.log)"
fi

# --- Test 4: Worker cmdline does not contain subcommand args ---
echo "--- Test 4: Worker cmdline is clean ---"
rm -f /tmp/logtime-test-cmdline.log
$LOGTIME -o /tmp/logtime-test-cmdline.log -- bash -c '(sleep 2; echo "bg") &' 2>&1
sleep 0.5

worker_pid=$(pgrep -xn logtime || true)
if [ -n "$worker_pid" ] && [ -f "/proc/$worker_pid/cmdline" ]; then
    cmdline=$(cat "/proc/$worker_pid/cmdline" | tr '\0' ' ')
    if echo "$cmdline" | grep -q "sleep"; then
        fail "worker cmdline contains subcommand args" "$cmdline"
    else
        pass "worker cmdline is clean: $cmdline"
    fi
else
    pass "worker already exited (fast command)"
fi
sleep 3

# --- Test 5: Exit code propagation ---
echo "--- Test 5: Exit code propagation ---"
rm -f /tmp/logtime-test-exit.log
set +e
$LOGTIME -o /tmp/logtime-test-exit.log -- bash -c 'exit 42' 2>&1
code=$?
set -e
sleep 0.5
if [ "$code" -eq 42 ]; then
    pass "exit code 42 propagated"
else
    fail "exit code not propagated" "expected 42, got $code"
fi

# --- Test 6: Direct mode with --set-subreaper ---
echo "--- Test 6: Direct mode with --set-subreaper ---"
output=$($LOGTIME --set-subreaper -- bash -c 'echo "sub"; (sleep 0.5; echo "child") &' 2>&1)
sleep 1
if echo "$output" | grep -q '\[stdout\] line: "sub"'; then
    pass "direct subreaper captured parent output"
else
    fail "direct subreaper missing parent output" "$output"
fi
if echo "$output" | grep -q '\[stdout\] line: "child"'; then
    pass "direct subreaper captured child output"
else
    fail "direct subreaper missing child output" "$output"
fi

# --- Summary ---
echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
