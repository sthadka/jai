#!/usr/bin/env zsh
set -euo pipefail

# Write-through feature test script
# Run from the jai repo root after building: go build -tags fts5 -o jai ./cmd/jai
# Usage: ./test-write-through.sh

JAI=(/Users/syethadk/go/bin/jai --config /Users/syethadk/.config/jai/staging.yaml --no-sync)
KEY="ROX-33601"  # "Prevent concurrent VM agent runs with lockfile guard" - status: To Do, priority: Normal

pass=0
fail=0
total=0

run_test() {
    local name="$1"
    shift
    total=$((total + 1))
    echo ""
    echo "━━━ Test $total: $name ━━━"
    echo "  \$ $*"
    if output=$("$@" 2>&1); then
        echo "  $output"
        echo "  ✓ PASS"
        pass=$((pass + 1))
    else
        echo "  $output"
        echo "  ✗ FAIL (exit code: $?)"
        fail=$((fail + 1))
    fi
}

check_output() {
    local name="$1"
    local expected="$2"
    shift 2
    total=$((total + 1))
    echo ""
    echo "━━━ Test $total: $name ━━━"
    echo "  \$ $*"
    output=$("$@" 2>&1) || true
    echo "  $output"
    if echo "$output" | grep -q "$expected"; then
        echo "  ✓ PASS (found: $expected)"
        pass=$((pass + 1))
    else
        echo "  ✗ FAIL (expected to find: $expected)"
        fail=$((fail + 1))
    fi
}

echo "═══════════════════════════════════════════════════"
echo "  Write-Through Feature Tests (staging)"
echo "  Test issue: $KEY"
echo "═══════════════════════════════════════════════════"

# ── Phase 0: Pre-flight ──────────────────────────────

check_output "Auth check" "✓" \
    "${JAI[@]}" status

# Push any stale pending changes first to start clean
echo ""
echo "━━━ Cleanup: pushing stale pending changes ━━━"
"${JAI[@]}" push 2>&1 || true

# ── Phase 1: jai set — write-through (default) ──────

check_output "set scalar field (write-through)" "✓" \
    "${JAI[@]}" set "$KEY" priority Medium

check_output "verify local DB updated" "Medium" \
    "${JAI[@]}" query "SELECT priority FROM issues WHERE key = '$KEY'"

check_output "set scalar field back (write-through)" "✓" \
    "${JAI[@]}" set "$KEY" priority Normal

# JSON output mode
check_output "set with --json (write-through)" '"status":"synced"' \
    "${JAI[@]}" set "$KEY" priority Medium --json

# Reset
"${JAI[@]}" set "$KEY" priority Normal > /dev/null 2>&1 || true

# ── Phase 2: jai set --queue ─────────────────────────

check_output "set with --queue" "queued" \
    "${JAI[@]}" set "$KEY" priority Major --queue

check_output "status shows pending change" "Pending changes:" \
    "${JAI[@]}" status

check_output "set --queue --json" '"status":"queued"' \
    "${JAI[@]}" set "$KEY" priority Normal --queue --json

check_output "push queued changes" "succeeded" \
    "${JAI[@]}" push

check_output "verify priority after push" "Normal" \
    "${JAI[@]}" query "SELECT priority FROM issues WHERE key = '$KEY'"

# ── Phase 3: jai comment — write-through ─────────────

check_output "comment (write-through)" "✓" \
    "${JAI[@]}" comment "$KEY" "write-through test $(date +%s)"

check_output "comment --json (write-through)" '"status":"synced"' \
    "${JAI[@]}" comment "$KEY" "write-through json test $(date +%s)" --json

# ── Phase 4: jai comment --queue ─────────────────────

check_output "comment --queue" "queued" \
    "${JAI[@]}" comment "$KEY" "queued test $(date +%s)" --queue

check_output "push queued comment" "succeeded" \
    "${JAI[@]}" push

# ── Phase 5: jai transition ──────────────────────────

echo ""
echo "━━━ Transition: listing available transitions ━━━"
"${JAI[@]}" transition "$KEY" --list 2>&1 || true

# Try transitioning — the issue is "To Do", try moving to "In Progress" if available
check_output "transition (write-through)" "✓" \
    "${JAI[@]}" transition "$KEY" "In Progress" \
    || echo "  (transition may not be available — check --list output above)"

# Move back if possible
"${JAI[@]}" transition "$KEY" "To Do" > /dev/null 2>&1 || true

# ── Phase 6: jai transition --queue ──────────────────

check_output "transition --queue" "queued" \
    "${JAI[@]}" transition "$KEY" "In Progress" --queue \
    || echo "  (transition may not be available)"

check_output "push queued transition" "succeeded" \
    "${JAI[@]}" push

# Move back
"${JAI[@]}" transition "$KEY" "To Do" > /dev/null 2>&1 || true

# ── Phase 7: Array fields (labels) ──────────────────

check_output "set array --add (write-through)" "✓" \
    "${JAI[@]}" set "$KEY" labels --add write-through-test

check_output "set array --remove (write-through)" "✓" \
    "${JAI[@]}" set "$KEY" labels --remove write-through-test

# ── Summary ──────────────────────────────────────────

echo ""
echo "═══════════════════════════════════════════════════"
echo "  Results: $pass passed, $fail failed, $total total"
echo "═══════════════════════════════════════════════════"

if [ "$fail" -gt 0 ]; then
    exit 1
fi
