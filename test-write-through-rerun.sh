#!/usr/bin/env zsh
set -euo pipefail

JAI=(/Users/syethadk/go/bin/jai --config /Users/syethadk/.config/jai/staging.yaml --no-sync)
KEY="ROX-33601"

pass=0
fail=0
total=0

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
echo "  Re-run: priority tests with valid value (Minor)"
echo "  Test issue: $KEY"
echo "═══════════════════════════════════════════════════"

# Test 2 re-run: set scalar field write-through
check_output "set priority Minor (write-through)" "✓" \
    "${JAI[@]}" set "$KEY" priority Minor

# Test 3 re-run: verify local DB updated
check_output "verify local DB updated to Minor" "Minor" \
    "${JAI[@]}" query "SELECT priority FROM issues WHERE key = '$KEY'"

# Test 5 re-run: JSON output
check_output "set --json (write-through)" '"status":"synced"' \
    "${JAI[@]}" set "$KEY" priority Minor --json

# Reset to Normal
"${JAI[@]}" set "$KEY" priority Normal > /dev/null 2>&1 || true

echo ""
echo "═══════════════════════════════════════════════════"
echo "  Results: $pass passed, $fail failed, $total total"
echo "═══════════════════════════════════════════════════"
