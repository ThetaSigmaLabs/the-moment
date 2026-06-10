#!/usr/bin/env bash
# =============================================================================
# test_stack.sh — Integration smoke tests for the The Moment + Spoolman stack
# =============================================================================
#
# This script:
#   1. Validates the docker-compose.yml file structure
#   2. Starts the stack
#   3. Waits for both services to be healthy
#   4. Runs API smoke tests against Spoolman and The Moment
#   5. Tears down cleanly
#
# Usage:
#   chmod +x test_stack.sh
#   ./test_stack.sh
#
# Requirements: docker, docker compose (v2), curl, jq
# =============================================================================

set -euo pipefail

# Always run from the project root (parent of this script's directory)
cd "$(dirname "$0")/.."

SPOOLMAN_URL="http://localhost:7912"
THE_MOMENT_URL="http://localhost:${THE_MOMENT_PORT:-5000}"
TIMEOUT_SECONDS=120  # How long to wait for healthy services

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

pass() { echo -e "${GREEN}✅  PASS${NC}: $1"; }
fail() { echo -e "${RED}❌  FAIL${NC}: $1"; FAILURES=$((FAILURES + 1)); }
info() { echo -e "${YELLOW}ℹ️   INFO${NC}: $1"; }

FAILURES=0

# =============================================================================
# Step 0 — Preflight checks
# =============================================================================
echo ""
info "Preflight checks..."

command -v docker  >/dev/null 2>&1 || { fail "docker not found"; exit 1; }
command -v curl    >/dev/null 2>&1 || { fail "curl not found"; exit 1; }
command -v jq      >/dev/null 2>&1 || { fail "jq not found"; exit 1; }

docker compose version >/dev/null 2>&1 || { fail "docker compose v2 not found"; exit 1; }
pass "All preflight tools present"

# =============================================================================
# Step 1 — Validate docker-compose.yml
# =============================================================================
echo ""
info "Validating docker-compose.yml..."

docker compose config --quiet 2>&1 && pass "docker-compose.yml syntax valid" \
  || { fail "docker-compose.yml failed validation"; exit 1; }

# Check required service names appear
for svc in spoolman the-moment; do
  docker compose config | grep -q "^  ${svc}:" && pass "Service '${svc}' present" \
    || fail "Service '${svc}' missing from docker-compose.yml"
done

# Check health conditions
docker compose config | grep -q "service_healthy" && pass "Dependency health check present" \
  || fail "the-moment does not depend on spoolman health"

# =============================================================================
# Step 2 — Start the stack
# =============================================================================
echo ""
info "Starting stack (this may take a minute on first run)..."

docker compose pull --quiet
docker compose up -d

# =============================================================================
# Step 3 — Wait for healthy status
# =============================================================================
echo ""
info "Waiting for services to become healthy (timeout: ${TIMEOUT_SECONDS}s)..."

wait_healthy() {
  local service="$1"
  local url="$2"
  local elapsed=0
  while [ $elapsed -lt $TIMEOUT_SECONDS ]; do
    if curl --silent --fail --max-time 5 "${url}" >/dev/null 2>&1; then
      pass "${service} is responding"
      return 0
    fi
    sleep 3
    elapsed=$((elapsed + 3))
    echo -n "."
  done
  echo ""
  fail "${service} did not become healthy within ${TIMEOUT_SECONDS}s"
  return 1
}

wait_healthy "Spoolman"   "${SPOOLMAN_URL}/api/v1/info"
wait_healthy "The Moment" "${THE_MOMENT_URL}/api/status"

# =============================================================================
# Step 4 — Spoolman API smoke tests
# =============================================================================
echo ""
info "Running Spoolman API smoke tests..."

# GET /api/v1/info
SM_INFO=$(curl -sf "${SPOOLMAN_URL}/api/v1/info" 2>/dev/null) || { fail "Spoolman /api/v1/info failed"; }
SM_VERSION=$(echo "${SM_INFO}" | jq -r '.version // "unknown"' 2>/dev/null)
[ "${SM_VERSION}" != "unknown" ] && pass "Spoolman version: ${SM_VERSION}" \
  || fail "Could not read Spoolman version from /api/v1/info"

# GET /api/v1/spool — should return an array (empty is fine)
SM_SPOOLS=$(curl -sf "${SPOOLMAN_URL}/api/v1/spool" 2>/dev/null) || { fail "Spoolman /api/v1/spool failed"; }
echo "${SM_SPOOLS}" | jq -e '. | type == "array"' >/dev/null 2>&1 \
  && pass "Spoolman /api/v1/spool returns valid JSON array" \
  || fail "Spoolman /api/v1/spool did not return a JSON array"

# GET /api/v1/filament
curl -sf "${SPOOLMAN_URL}/api/v1/filament" >/dev/null 2>&1 \
  && pass "Spoolman /api/v1/filament reachable" \
  || fail "Spoolman /api/v1/filament failed"

# GET /api/v1/location
curl -sf "${SPOOLMAN_URL}/api/v1/location" >/dev/null 2>&1 \
  && pass "Spoolman /api/v1/location reachable" \
  || fail "Spoolman /api/v1/location failed"

# POST — Create a test filament vendor
TEST_VENDOR=$(curl -sf -X POST "${SPOOLMAN_URL}/api/v1/vendor" \
  -H "Content-Type: application/json" \
  -d '{"name":"The MomentTestVendor"}' 2>/dev/null) || { fail "Spoolman POST /api/v1/vendor failed"; }
VENDOR_ID=$(echo "${TEST_VENDOR}" | jq -r '.id // 0')
[ "${VENDOR_ID}" != "0" ] && pass "Created test vendor (id=${VENDOR_ID})" \
  || fail "Vendor creation returned unexpected response"

# POST — Create a test filament
TEST_FILAMENT=$(curl -sf -X POST "${SPOOLMAN_URL}/api/v1/filament" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"The MomentTestFilament\",\"vendor_id\":${VENDOR_ID},\"material\":\"PLA\",\"density\":1.24,\"diameter\":1.75}" \
  2>/dev/null) || { fail "Spoolman POST /api/v1/filament failed"; }
FILAMENT_ID=$(echo "${TEST_FILAMENT}" | jq -r '.id // 0')
[ "${FILAMENT_ID}" != "0" ] && pass "Created test filament (id=${FILAMENT_ID})" \
  || fail "Filament creation returned unexpected response"

# POST — Create a test spool (1kg, 200g used)
TEST_SPOOL=$(curl -sf -X POST "${SPOOLMAN_URL}/api/v1/spool" \
  -H "Content-Type: application/json" \
  -d "{\"filament_id\":${FILAMENT_ID},\"initial_weight\":1000,\"used_weight\":200}" \
  2>/dev/null) || { fail "Spoolman POST /api/v1/spool failed"; }
SPOOL_ID=$(echo "${TEST_SPOOL}" | jq -r '.id // 0')
[ "${SPOOL_ID}" != "0" ] && pass "Created test spool (id=${SPOOL_ID}, 800g remaining)" \
  || fail "Spool creation returned unexpected response"

# PATCH — Simulate The Moment updating spool usage (use 50g)
PATCH_RESULT=$(curl -sf -X PATCH "${SPOOLMAN_URL}/api/v1/spool/${SPOOL_ID}" \
  -H "Content-Type: application/json" \
  -d '{"used_weight":250}' 2>/dev/null) || { fail "Spoolman PATCH /api/v1/spool failed"; }
REMAINING=$(echo "${PATCH_RESULT}" | jq -r '.remaining_weight // -1')
echo "${REMAINING}" | grep -qE '^7[0-9]{2}' \
  && pass "Spool usage update correct: ${REMAINING}g remaining" \
  || fail "Spool remaining weight unexpected after PATCH: ${REMAINING}"

# =============================================================================
# Step 5 — The Moment API smoke tests
# =============================================================================
echo ""
info "Running The Moment API smoke tests..."

# GET /api/status
FB_STATUS=$(curl -sf "${THE_MOMENT_URL}/api/status" 2>/dev/null) || { fail "The Moment /api/status failed"; }
echo "${FB_STATUS}" | jq -e '.printers' >/dev/null 2>&1 \
  && pass "The Moment /api/status returns valid JSON with 'printers' key" \
  || fail "The Moment /api/status missing 'printers' key"

# GET /api/spools — should proxy Spoolman spool list
FB_SPOOLS=$(curl -sf "${THE_MOMENT_URL}/api/spools" 2>/dev/null) || { fail "The Moment /api/spools failed"; }
echo "${FB_SPOOLS}" | jq -e '. | type == "array"' >/dev/null 2>&1 \
  && pass "The Moment /api/spools returns JSON array" \
  || fail "The Moment /api/spools did not return a JSON array"

# GET /api/print-errors — verify error tracking endpoint
curl -sf "${THE_MOMENT_URL}/api/print-errors" >/dev/null 2>&1 \
  && pass "The Moment /api/print-errors reachable" \
  || fail "The Moment /api/print-errors failed"

# WebSocket endpoint existence (test with HTTP — will 400 without upgrade, which confirms it's there)
WS_CODE=$(curl -so /dev/null -w "%{http_code}" "${THE_MOMENT_URL}/ws/status" 2>/dev/null)
[ "${WS_CODE}" = "400" ] || [ "${WS_CODE}" = "101" ] \
  && pass "The Moment WebSocket endpoint present (HTTP ${WS_CODE})" \
  || fail "The Moment WebSocket endpoint unexpected response: HTTP ${WS_CODE}"

# Verify The Moment can reach Spoolman (internal Docker network)
# This is confirmed if /api/spools returns data matching what we created
FB_SPOOL_COUNT=$(echo "${FB_SPOOLS}" | jq 'length')
[ "${FB_SPOOL_COUNT}" -gt "0" ] \
  && pass "The Moment can reach Spoolman — sees ${FB_SPOOL_COUNT} spool(s)" \
  || fail "The Moment /api/spools returned 0 spools — may not be reaching Spoolman"

# =============================================================================
# Step 6 — Cleanup test data
# =============================================================================
echo ""
info "Cleaning up test data..."

curl -sf -X DELETE "${SPOOLMAN_URL}/api/v1/spool/${SPOOL_ID}"   >/dev/null 2>&1 && pass "Removed test spool" || true
curl -sf -X DELETE "${SPOOLMAN_URL}/api/v1/filament/${FILAMENT_ID}" >/dev/null 2>&1 && pass "Removed test filament" || true
curl -sf -X DELETE "${SPOOLMAN_URL}/api/v1/vendor/${VENDOR_ID}"  >/dev/null 2>&1 && pass "Removed test vendor" || true

# =============================================================================
# Step 7 — Summary
# =============================================================================
echo ""
echo "=============================================="
if [ "${FAILURES}" -eq 0 ]; then
  echo -e "${GREEN}ALL TESTS PASSED${NC}"
  echo ""
  echo "Stack is running:"
  echo "  Spoolman   → ${SPOOLMAN_URL}"
  echo "  The Moment → ${THE_MOMENT_URL}"
  echo ""
  echo "Next steps:"
  echo "  1. Open The Moment at ${THE_MOMENT_URL} and add your Core One L"
  echo "  2. Configure Moonraker on your Ender 3 V3 SE to point to ${SPOOLMAN_URL}"
  echo "  3. Add your filament spools in Spoolman at ${SPOOLMAN_URL}"
else
  echo -e "${RED}${FAILURES} TEST(S) FAILED${NC}"
  echo "Run 'docker compose logs' to investigate."
  docker compose logs --tail=50
fi
echo "=============================================="

exit "${FAILURES}"
