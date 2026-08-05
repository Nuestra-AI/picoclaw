#!/usr/bin/env bash
# End-to-end smoke test for magicform multi-tenancy.
#
# Verifies, against a running gateway, that:
#   1. The CLI tenant flow works: `picoclaw agent --workspace ... --config-dir ...`
#      lands its session in the tenant's workspace, not the default.
#   2. The webhook flow works: a POST to /hooks/<channel-key> with workspace +
#      configDir hints provisions a per-tenant agent.
#   3. Two distinct tenants produce isolated session directories.
#   4. The fail-closed behaviour fires: a webhook with a path-escape
#      attempt returns an error and does not create a workspace outside
#      the boundary.
#
# Modes:
#
#   ./deploy/smoke-test.sh                  # local mode (default)
#       Builds the binary if needed, expects you've already started a
#       gateway via `picoclaw gateway` in another terminal.
#
#   DEPLOY_MODE=docker ./deploy/smoke-test.sh
#       Expects a running compose stack from
#       deploy/docker-compose.magicform.yml.
#
# Configuration via env (with defaults for the local-dev case):
#
#   DEPLOY_MODE             local | docker        (default: local)
#   GATEWAY_URL             http://127.0.0.1:18790
#   WORKSPACE_ROOT          /data/workspaces
#   CHANNEL_KEY             magicform            (health/webhook path segment)
#   CHANNEL_TOKEN           dev-shared-secret    (or MAGICFORM_TOKEN, legacy)
#   PICOCLAW_BIN            ./build/picoclaw   (local mode only)
#   CALLBACK_PORT           19999              (local-only mock receiver)
#   PROVISION_TIMEOUT       30                 (seconds to await provisioning)
#   KEEP_DIRS               unset              (set to keep tenant dirs)
#
# Exits non-zero on any failure. Prints a clear PASS/FAIL line per check.

set -uo pipefail

# Colors only when stdout is a TTY.
if [[ -t 1 ]]; then
  C_OK=$'\033[32m'; C_FAIL=$'\033[31m'; C_DIM=$'\033[2m'; C_RESET=$'\033[0m'
else
  C_OK=""; C_FAIL=""; C_DIM=""; C_RESET=""
fi

DEPLOY_MODE="${DEPLOY_MODE:-local}"
GATEWAY_URL="${GATEWAY_URL:-http://127.0.0.1:18790}"
WORKSPACE_ROOT="${WORKSPACE_ROOT:-/data/workspaces}"
# The gateway derives /hooks/<key> and /health/<key> from the channel's key in
# channel_list, so point this at whatever the deployment uses.
CHANNEL_KEY="${CHANNEL_KEY:-magicform}"
# MAGICFORM_TOKEN is the older name for this and still works.
CHANNEL_TOKEN="${CHANNEL_TOKEN:-${MAGICFORM_TOKEN:-dev-shared-secret}}"
PICOCLAW_BIN="${PICOCLAW_BIN:-./build/picoclaw}"
CALLBACK_PORT="${CALLBACK_PORT:-19999}"
# How long to wait for asynchronous tenant provisioning before giving up.
PROVISION_TIMEOUT="${PROVISION_TIMEOUT:-30}"
# Must match container_name in deploy/docker-compose.magicform.yml.
CONTAINER_NAME="${CONTAINER_NAME:-picoclaw-magicform}"

PASS_COUNT=0
FAIL_COUNT=0
SKIP_COUNT=0
FAIL_NAMES=()
SKIP_NAMES=()

step() {
  printf "%s── %s%s\n" "$C_DIM" "$1" "$C_RESET"
}

ok() {
  printf "  %sPASS%s  %s\n" "$C_OK" "$C_RESET" "$1"
  PASS_COUNT=$((PASS_COUNT + 1))
}

fail() {
  printf "  %sFAIL%s  %s\n" "$C_FAIL" "$C_RESET" "$1"
  FAIL_COUNT=$((FAIL_COUNT + 1))
  FAIL_NAMES+=("$1")
}

# A check that could not run for environmental reasons - not a pass, but not
# a failure of the code under test either. Reported separately so an
# unconfigured box does not look broken.
skip() {
  printf "  %sSKIP%s  %s\n" "$C_DIM" "$C_RESET" "$1"
  SKIP_COUNT=$((SKIP_COUNT + 1))
  SKIP_NAMES+=("$1")
}

# Verify host prerequisites before touching anything.
require() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "smoke-test: required tool '$1' not found in PATH" >&2
    exit 2
  }
}
require curl
require jq

step "Mode: $DEPLOY_MODE   Gateway: $GATEWAY_URL   Root: $WORKSPACE_ROOT"

# Verify the gateway is reachable before doing any work; otherwise the
# user gets a wall of red FAIL lines that all just mean "gateway down".
if ! curl -fsS --max-time 3 "$GATEWAY_URL/health/$CHANNEL_KEY" >/dev/null 2>&1; then
  echo "smoke-test: gateway not reachable at $GATEWAY_URL — start it first" >&2
  echo "  local:  $PICOCLAW_BIN gateway -d" >&2
  echo "  docker: docker compose -f deploy/docker-compose.magicform.yml up -d" >&2
  exit 2
fi

# ── Tenant directory setup ──────────────────────────────────────────
TENANT_A="smoke-tenant-a-$$"
TENANT_B="smoke-tenant-b-$$"
WORKSPACE_A="$WORKSPACE_ROOT/$TENANT_A/workspace"
WORKSPACE_B="$WORKSPACE_ROOT/$TENANT_B/workspace"
CONFIG_A="$WORKSPACE_ROOT/$TENANT_A/config"
CONFIG_B="$WORKSPACE_ROOT/$TENANT_B/config"

step "Provisioning tenant directories under $WORKSPACE_ROOT"
mkdir -p "$WORKSPACE_A" "$WORKSPACE_B" "$CONFIG_A" "$CONFIG_B" || {
  echo "smoke-test: cannot create tenant dirs under $WORKSPACE_ROOT" >&2
  echo "  ensure $WORKSPACE_ROOT exists and you can write to it" >&2
  exit 2
}

# Cleanup on exit unless KEEP_DIRS is set (handy for post-mortem).
cleanup() {
  if [[ -z "${KEEP_DIRS:-}" ]]; then
    rm -rf "$WORKSPACE_ROOT/$TENANT_A" "$WORKSPACE_ROOT/$TENANT_B" 2>/dev/null || true
  fi
  if [[ -n "${CALLBACK_PID:-}" ]]; then
    kill "$CALLBACK_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

# Bootstrap content — we'll verify it lands in the workspace post-turn.
echo "You are tenant A's assistant." > "$CONFIG_A/AGENT.md"
echo "You are tenant B's assistant." > "$CONFIG_B/AGENT.md"

# ── Test 1: CLI tenant flow (local mode only — needs the binary) ───
if [[ "$DEPLOY_MODE" == "local" ]]; then
  step "Test 1: CLI tenant flow lands sessions in the tenant workspace"

  if [[ ! -x "$PICOCLAW_BIN" ]]; then
    fail "CLI tenant flow (binary not found at $PICOCLAW_BIN; run 'make build' first)"
  else
    # Fire a one-shot agent message scoped to tenant A. We don't care about
    # the LLM response (provider may not be configured); we care that the
    # gateway/CLI accepted the workspace + config-dir flags and routed the
    # session under the tenant path.
    "$PICOCLAW_BIN" agent \
      -m "smoke test message" \
      -s "smoke:tenant-a" \
      --workspace "$TENANT_A/workspace" \
      --config-dir "$TENANT_A/config" \
      >/tmp/smoke-cli.out 2>&1 || true

    if find "$WORKSPACE_A/sessions" -mindepth 1 -maxdepth 2 -type f 2>/dev/null | grep -q .; then
      ok "CLI created a session under $WORKSPACE_A/sessions/"
    else
      fail "CLI did not create a session under $WORKSPACE_A/sessions/ (see /tmp/smoke-cli.out)"
    fi
  fi
else
  step "Test 1: CLI tenant flow — skipped in docker mode (run locally to exercise)"
fi

# ── Mock callback receiver (background) ────────────────────────────
# Webhook test 2 sends a callbackUrl that points at this listener; we
# don't assert on its content here, but we keep it alive so the agent
# loop's POST doesn't fail with "connection refused" in logs (which
# would drown actual errors).
if command -v python3 >/dev/null 2>&1; then
  step "Starting mock callback receiver on :$CALLBACK_PORT"
  python3 -c "
import http.server, sys
class H(http.server.BaseHTTPRequestHandler):
    def log_message(self,*a,**k): pass
    def do_POST(self):
        self.send_response(200); self.end_headers()
http.server.HTTPServer(('127.0.0.1', $CALLBACK_PORT), H).serve_forever()
" >/dev/null 2>&1 &
  CALLBACK_PID=$!
  sleep 0.3
fi

# post_tenant_webhook <label> <stack-id> <tenant-dir> <body-file>
#
# Sends one tenant webhook and asserts on the status. Both tenants go through
# this so neither can silently skip its assertion.
post_tenant_webhook() {
  local label="$1" stack="$2" tenant="$3" body_file="$4"
  local status

  status=$(curl -sS -o "$body_file" -w "%{http_code}" \
    -X POST "$GATEWAY_URL/hooks/$CHANNEL_KEY" \
    -H "Authorization: Bearer $CHANNEL_TOKEN" \
    -H "Content-Type: application/json" \
    -d "{
      \"stackId\": \"$stack\",
      \"conversationId\": \"$stack-$$\",
      \"userId\": \"smoke-user\",
      \"message\": \"smoke test for $label\",
      \"workspace\": \"$tenant/workspace\",
      \"configDir\": \"$tenant/config\",
      \"callbackUrl\": \"http://127.0.0.1:$CALLBACK_PORT/cb\"
    }")

  if [[ "$status" == "200" || "$status" == "202" ]]; then
    ok "Webhook for $label accepted (HTTP $status)"
  else
    fail "Webhook for $label returned HTTP $status (body: $(cat "$body_file" 2>/dev/null))"
  fi
}

# wait_for_tenant_sessions <workspace> <timeout-seconds>
#
# Provisioning is asynchronous, so poll instead of sleeping a fixed interval:
# a fixed sleep is simultaneously flaky on a loaded box and needlessly slow on
# an idle one. Returns 0 as soon as a session file appears.
wait_for_tenant_sessions() {
  local workspace="$1" timeout="${2:-30}" waited=0
  while (( waited < timeout )); do
    if find "$workspace/sessions" -mindepth 1 -type f 2>/dev/null | grep -q .; then
      return 0
    fi
    sleep 1
    waited=$((waited + 1))
  done
  return 1
}

# ── Test 2: Webhook tenant provisioning ─────────────────────────────
step "Test 2: Webhook for tenant A provisions a tenant agent"
post_tenant_webhook "tenant A" "smoke-a" "$TENANT_A" /tmp/smoke-wh-a.body

# ── Test 3: Second tenant accepted ──────────────────────────────────
step "Test 3: Webhook for tenant B is accepted"
post_tenant_webhook "tenant B" "smoke-b" "$TENANT_B" /tmp/smoke-wh-b.body

# Wait for both tenants to provision rather than sleeping a fixed interval.
# Not finding sessions is not itself a failure here - Test 5 distinguishes
# "no provider configured" from "isolation broken".
step "Waiting up to ${PROVISION_TIMEOUT}s for tenant provisioning"
wait_for_tenant_sessions "$WORKSPACE_A" "$PROVISION_TIMEOUT" || true
wait_for_tenant_sessions "$WORKSPACE_B" "$PROVISION_TIMEOUT" || true

# ── Test 4: Bootstrap files copied ─────────────────────────────────
step "Test 4: Bootstrap files copied into tenant workspaces"

if [[ -f "$WORKSPACE_A/AGENT.md" ]] && grep -q "tenant A" "$WORKSPACE_A/AGENT.md"; then
  ok "AGENT.md present in tenant A's workspace"
else
  fail "Tenant A's AGENT.md missing (provisionBootstrapFiles did not fire — was first turn rejected upstream?)"
fi

if [[ -f "$WORKSPACE_B/AGENT.md" ]] && grep -q "tenant B" "$WORKSPACE_B/AGENT.md"; then
  ok "AGENT.md present in tenant B's workspace"
else
  fail "Tenant B's AGENT.md missing"
fi

# ── Test 5: Sessions are isolated ───────────────────────────────────
step "Test 5: Tenant A and B sessions live in separate directories"

A_SESSIONS=$(find "$WORKSPACE_A/sessions" -mindepth 1 -type f 2>/dev/null | wc -l)
B_SESSIONS=$(find "$WORKSPACE_B/sessions" -mindepth 1 -type f 2>/dev/null | wc -l)

if [[ "$A_SESSIONS" -gt 0 && "$B_SESSIONS" -gt 0 ]]; then
  # Distinct directories by construction; confirm neither leaked into the
  # other, which is the property that actually matters.
  if find "$WORKSPACE_A/sessions" -mindepth 1 -type f 2>/dev/null |
      grep -q "$TENANT_B"; then
    fail "Tenant B content found under tenant A's session directory"
  else
    ok "Both tenants have isolated session files (A=$A_SESSIONS, B=$B_SESSIONS)"
  fi
elif [[ "$A_SESSIONS" -eq 0 && "$B_SESSIONS" -eq 0 ]]; then
  # No sessions at all almost always means the gateway has no reachable LLM
  # provider. That is an environment gap, not an isolation failure - calling
  # it FAIL would mean an unconfigured box reports a red result for code that
  # is fine.
  skip "Neither tenant created sessions - no LLM provider reachable? Isolation untested"
else
  fail "Asymmetric: A=$A_SESSIONS sessions, B=$B_SESSIONS sessions (one tenant provisioned, the other did not)"
fi

# ── Test 6: Path escape rejected ────────────────────────────────────
step "Test 6: Path-escape attempt is rejected (fail-closed)"

ESCAPE_RESPONSE=$(curl -sS -o /tmp/smoke-escape.body -w "%{http_code}" \
  -X POST "$GATEWAY_URL/hooks/$CHANNEL_KEY" \
  -H "Authorization: Bearer $CHANNEL_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{
    \"stackId\": \"smoke-escape\",
    \"conversationId\": \"smoke-escape-$$\",
    \"userId\": \"smoke-user\",
    \"message\": \"escape attempt\",
    \"workspace\": \"../../../etc\",
    \"configDir\": \"../../../etc\",
    \"callbackUrl\": \"http://127.0.0.1:$CALLBACK_PORT/cb\"
  }")

# The webhook handler validates at ingress and returns 4xx for path
# escape. We accept 400-499 as "rejected"; anything in 200/500 is wrong.
if [[ "$ESCAPE_RESPONSE" =~ ^4[0-9][0-9]$ ]]; then
  ok "Path escape attempt rejected with HTTP $ESCAPE_RESPONSE"
else
  fail "Path escape returned HTTP $ESCAPE_RESPONSE (expected 4xx)"
fi

# ── Test 7: Distinct hashed agent IDs in logs ──────────────────────
# Only meaningful in docker mode, where the container log is reachable.
#
# The gateway derives a tenant's agent ID as "tenant-<sha256(workspace)[:4]>"
# (deriveTenantAgentID in pkg/agent/agent_tenant_registry.go). The ID is a
# hash, so it never appears next to the tenant's name in the log - we compute
# the expected IDs here and look for those exact strings.
if [[ "$DEPLOY_MODE" == "docker" ]]; then
  step "Test 7: Distinct hashed agent IDs in gateway log"

  if ! command -v docker >/dev/null 2>&1; then
    fail "Test 7 needs the docker CLI to read container logs"
  elif ! command -v sha256sum >/dev/null 2>&1; then
    fail "Test 7 needs sha256sum to derive the expected tenant agent IDs"
  else
    # Mirror deriveTenantAgentID: sha256 of filepath.Clean(workspace), first
    # 4 bytes as hex. The paths built above are already absolute and clean,
    # so Clean is a no-op on them - and the gateway runs in a Linux
    # container, where Clean keeps forward slashes. (Running the gateway on
    # Windows would rewrite them to backslashes and change the digest, but
    # this test only runs in docker mode.)
    tenant_agent_id() {
      printf '%s' "$1" | sha256sum | cut -c1-8 | sed 's/^/tenant-/'
    }

    A_ID=$(tenant_agent_id "$WORKSPACE_A")
    B_ID=$(tenant_agent_id "$WORKSPACE_B")
    LOG=$(docker logs "$CONTAINER_NAME" 2>&1 | tail -n 2000)

    if [[ "$A_ID" == "$B_ID" ]]; then
      fail "Derived identical agent IDs for two workspaces ($A_ID) - hashing is broken"
    elif grep -q "$A_ID" <<<"$LOG" && grep -q "$B_ID" <<<"$LOG"; then
      ok "Both tenants logged with distinct agent IDs ($A_ID, $B_ID)"
    elif grep -q "$A_ID" <<<"$LOG" || grep -q "$B_ID" <<<"$LOG"; then
      fail "Only one tenant agent ID appears in the log (expected $A_ID and $B_ID)"
    else
      # Same reasoning as Test 5: no agents provisioned at all is an
      # environment gap rather than a defect in ID derivation.
      skip "Neither tenant agent ID in the log (expected $A_ID, $B_ID) - were any agents provisioned?"
    fi
  fi
fi

# ── Summary ─────────────────────────────────────────────────────────
echo
TOTAL=$((PASS_COUNT + FAIL_COUNT + SKIP_COUNT))

if (( SKIP_COUNT > 0 )); then
  printf "%s%d check(s) skipped:%s\n" "$C_DIM" "$SKIP_COUNT" "$C_RESET"
  for n in "${SKIP_NAMES[@]}"; do
    printf "  %s•%s %s\n" "$C_DIM" "$C_RESET" "$n"
  done
fi

if [[ "$FAIL_COUNT" -eq 0 ]]; then
  if (( SKIP_COUNT > 0 )); then
    printf "%s%d/%d checks passed, %d skipped.%s\n" \
      "$C_OK" "$PASS_COUNT" "$TOTAL" "$SKIP_COUNT" "$C_RESET"
  else
    printf "%sAll %d checks passed.%s\n" "$C_OK" "$PASS_COUNT" "$C_RESET"
  fi
  exit 0
else
  printf "%s%d/%d checks failed:%s\n" "$C_FAIL" "$FAIL_COUNT" "$TOTAL" "$C_RESET"
  for n in "${FAIL_NAMES[@]}"; do
    printf "  %s•%s %s\n" "$C_FAIL" "$C_RESET" "$n"
  done
  exit 1
fi
