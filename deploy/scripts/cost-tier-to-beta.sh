#!/usr/bin/env bash
#
# cost-tier-to-beta.sh — cut Eurobase's Scaleway bill from ~€127/mo to ~€45/mo.
#
# Companion to docs/runbooks/cost-tier.md and PR #136. Runs the three
# post-merge operator steps in sequence, with verification + a final
# confirmation prompt before the only destructive action (deleting the
# managed Redis instance).
#
# What this does:
#   1. Patches REDIS_URL in the eurobase-secrets Secret to point at the
#      in-cluster Redis (deploy/k8s/redis.yaml, already applied by the
#      PR #136 deploy). Restarts gateway + worker so they pick up the
#      new URL, then greps logs to confirm the connection succeeded.
#   2. Sets the Kapsule pool autoscaler to min=1, max=1 and resizes it
#      to a single DEV1-M node. Waits for the second node to drain and
#      disappear.
#   3. After explicit confirmation, deletes the Scaleway managed Redis
#      cluster (RED1-micro). This is the only irreversible step.
#
# Prereqs:
#   - kubectl is configured for the production cluster
#   - scw is configured for the production project
#   - In-cluster Redis StatefulSet is already running (PR #136 deploy)
#
# Run from a machine with both CLIs configured:
#   bash deploy/scripts/cost-tier-to-beta.sh

set -euo pipefail

# Hard-coded resource identifiers (verified 2026-05-16). Update these
# if cluster / pool / managed-redis IDs ever change.
NAMESPACE="eurobase"
SECRET_NAME="eurobase-secrets"
NEW_REDIS_URL="redis://redis.eurobase.svc.cluster.local:6379"
POOL_ID="7ce10d4d-0e8e-44ec-9bac-44cdfa9d04e7"
MANAGED_REDIS_ID="c936f69c-9a95-4269-aa12-5fd9637931c0"
SCW_REGION="fr-par"
SCW_ZONE="fr-par-1"

bold()  { printf '\033[1m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
yellow(){ printf '\033[33m%s\033[0m\n' "$*"; }
red()   { printf '\033[31m%s\033[0m\n' "$*"; }
die()   { red "ERROR: $*"; exit 1; }

require_tool() {
  command -v "$1" >/dev/null 2>&1 || die "$1 is required but not on PATH"
}

require_tool kubectl
require_tool scw
require_tool base64

# Sanity-check: in-cluster Redis exists and is Ready.
bold "Pre-flight: verifying in-cluster Redis is up..."
if ! kubectl get statefulset redis -n "$NAMESPACE" >/dev/null 2>&1; then
  die "StatefulSet redis/$NAMESPACE not found. Apply deploy/k8s/redis.yaml first (PR #136 should have done this)."
fi
ready=$(kubectl get statefulset redis -n "$NAMESPACE" -o jsonpath='{.status.readyReplicas}')
if [[ "${ready:-0}" != "1" ]]; then
  die "redis StatefulSet not ready (readyReplicas=$ready). Check kubectl describe statefulset redis -n $NAMESPACE"
fi

# Reachability check from a one-shot pod. Avoids depending on the
# gateway being up.
bold "Pre-flight: pinging in-cluster Redis..."
ping_out=$(kubectl run -n "$NAMESPACE" --rm -it --restart=Never \
  redis-connect-check --image=redis:7-alpine --quiet \
  -- redis-cli -h redis.eurobase.svc.cluster.local ping 2>&1 | tail -2 | grep -i pong || true)
[[ -n "$ping_out" ]] || die "Redis did not respond to PING from a fresh pod. Aborting."
green "  in-cluster Redis answers PONG"
echo

# ── Step 1 — cut over REDIS_URL ──────────────────────────────────────
bold "Step 1/3: cutting REDIS_URL over to the in-cluster service"

current_url=$(kubectl get secret "$SECRET_NAME" -n "$NAMESPACE" \
  -o jsonpath='{.data.REDIS_URL}' | base64 -d 2>/dev/null || true)
echo "  current REDIS_URL: ${current_url:-<empty>}"
echo "  new     REDIS_URL: $NEW_REDIS_URL"

if [[ "$current_url" == "$NEW_REDIS_URL" ]]; then
  yellow "  already set; skipping patch."
else
  # Build the JSON patch with the base64-encoded new value. Pipeline form
  # (printf → base64 → tr → xargs) avoids the shell-scope footguns that
  # bit the manual attempts.
  printf '%s' "$NEW_REDIS_URL" \
    | base64 \
    | tr -d '\n' \
    | xargs -I@ kubectl patch secret "$SECRET_NAME" -n "$NAMESPACE" \
        --type=json \
        -p '[{"op":"replace","path":"/data/REDIS_URL","value":"@"}]' \
    >/dev/null
  written=$(kubectl get secret "$SECRET_NAME" -n "$NAMESPACE" \
    -o jsonpath='{.data.REDIS_URL}' | base64 -d)
  [[ "$written" == "$NEW_REDIS_URL" ]] || die "patch did not stick (got: '$written')"
  green "  secret patched."
fi

bold "  rolling gateway + worker..."
kubectl rollout restart deployment/gateway deployment/worker -n "$NAMESPACE" >/dev/null
kubectl rollout status   deployment/gateway -n "$NAMESPACE" --timeout=180s
kubectl rollout status   deployment/worker  -n "$NAMESPACE" --timeout=180s

bold "  checking gateway logs for the redis-connect line..."
# The Go gateway logs structured JSON. Look for either of the two
# success messages from internal/ratelimit/limiter.go:71 or
# internal/realtime/redis.go:39.
sleep 4   # give the new pods a beat to emit the line
log_hit=$(kubectl logs -n "$NAMESPACE" -l app=gateway --tail=400 --all-containers=true 2>&1 \
  | grep -iE 'rate limiter redis connected|realtime redis bridge connected' || true)
if [[ -z "$log_hit" ]]; then
  red "  no 'redis connected' log line found in the last 400 lines."
  red "  Gateway may have fallen back to disabled rate limiting."
  echo "  Run:  kubectl logs -n $NAMESPACE -l app=gateway --tail=400 | grep -iE 'redis|rate.*limit'"
  die "step 1 verification failed"
fi
green "  gateway connected to redis:"
echo "$log_hit" | sed 's/^/    /'
echo

# ── Step 2 — shrink Kapsule pool to 1 node ───────────────────────────
bold "Step 2/3: shrinking Kapsule pool to a single node"

echo "  pool: $POOL_ID (region $SCW_REGION)"

# Idempotent: set autoscaler bounds first so the resize doesn't get
# bounced back up before we can shrink. `scw k8s pool update` exposes
# the autoscaler fields as top-level `min-size` / `max-size` (NOT
# under an `autoscaler.` prefix — that was my first guess and bombed
# with "Unknown argument 'autoscaler.min-size'" on the live run).
scw k8s pool update "$POOL_ID" region="$SCW_REGION" \
  min-size=1 \
  max-size=1 >/dev/null
green "  autoscaler set to min=1 max=1."

scw k8s pool update "$POOL_ID" region="$SCW_REGION" size=1 >/dev/null
green "  resize requested (size=1)."

bold "  waiting for the second node to drain and disappear (up to 6 min)..."
deadline=$(( $(date +%s) + 360 ))
while [[ $(date +%s) -lt $deadline ]]; do
  count=$(kubectl get nodes --no-headers 2>/dev/null | wc -l | tr -d ' ')
  echo "    nodes: $count"
  if [[ "$count" == "1" ]]; then
    green "  pool is down to a single node."
    break
  fi
  sleep 20
done
final_count=$(kubectl get nodes --no-headers 2>/dev/null | wc -l | tr -d ' ')
if [[ "$final_count" != "1" ]]; then
  red "  still $final_count nodes after 6 min. The drain may be stuck on a PodDisruptionBudget or a stateful workload."
  echo "  Check: kubectl get nodes; kubectl describe nodes; kubectl get pdb -A"
  die "step 2 did not converge — bail before deleting managed Redis"
fi
echo

# ── Step 3 — delete the managed Redis cluster ────────────────────────
bold "Step 3/3: delete the managed Scaleway Redis cluster ($MANAGED_REDIS_ID)"
yellow "  This is the only irreversible action in this script."
yellow "  Confirm again that the gateway is using the in-cluster Redis: the log line"
yellow "  above showed a Run-time connect to redis.eurobase.svc.cluster.local."
echo
read -r -p "  Type 'delete managed redis' to proceed: " confirm
if [[ "$confirm" != "delete managed redis" ]]; then
  yellow "  skipped — managed Redis is still alive."
  echo "  Re-run this script (it's idempotent) or delete manually:"
  echo "    scw redis cluster delete $MANAGED_REDIS_ID zone=$SCW_ZONE"
  exit 0
fi

scw redis cluster delete "$MANAGED_REDIS_ID" zone="$SCW_ZONE" >/dev/null
green "  managed Redis deletion requested."

bold "Done."
echo "  Watch the Scaleway billing console — the RED1-micro line should disappear"
echo "  within minutes. Expect the next monthly bill at ~€45 (down from ~€127)."
echo
echo "  When the first paying Pro customer signs up, see issue #135 + the"
echo "  'Beta → production' section of docs/runbooks/cost-tier.md to scale back up."
