#!/usr/bin/env bash
# TLS certificate freshness check for *.eurobase.app and platform hosts.
#
# Exit codes:
#   0  all certificates valid for ≥ WARN_DAYS (default 14)
#   1  at least one certificate expires within WARN_DAYS but is still valid today
#   2  at least one certificate is already expired or the host is unreachable
#
# Designed to be portable: only `openssl s_client` + `openssl x509 -checkend`.
# No GNU/BSD date math, no jq.
#
# Same script body runs:
#   - locally from a dev machine (smoke test before/after a release)
#   - from CI post-deploy (`./scripts/ops/cert-status.sh`)
#   - from the in-cluster CronJob (deploy/k8s/cert-monitor-cronjob.yaml) — which
#     captures the output and posts to Discord on non-zero exit.
#
# Usage:
#   ./scripts/ops/cert-status.sh                # default host list + 14d warn
#   WARN_DAYS=7 ./scripts/ops/cert-status.sh    # tighter window
#   ./scripts/ops/cert-status.sh foo.example    # ad-hoc hosts override

set -u

# Default hosts: platform surfaces + two canary tenants on the wildcard.
# Add or remove freely — every entry is checked independently and the worst
# status wins.
DEFAULT_HOSTS=(
  "api.eurobase.app"
  "console.eurobase.app"
  "mcp.eurobase.app"
  "newtek2.eurobase.app"
  "livestylist.eurobase.app"
)

WARN_DAYS="${WARN_DAYS:-14}"
WARN_SECS=$(( WARN_DAYS * 86400 ))

if [ "$#" -gt 0 ]; then
  HOSTS=("$@")
else
  HOSTS=("${DEFAULT_HOSTS[@]}")
fi

worst=0  # 0=ok, 1=warn, 2=critical
report=""

for host in "${HOSTS[@]}"; do
  cert=$(echo | openssl s_client -servername "$host" -connect "$host:443" \
    -showcerts 2>/dev/null | openssl x509 2>/dev/null)
  if [ -z "$cert" ]; then
    report="${report}CRITICAL  ${host}  unreachable / no certificate served"$'\n'
    [ "$worst" -lt 2 ] && worst=2
    continue
  fi

  # Pull the human-readable notAfter for the report only — the gating
  # decisions use openssl -checkend, which is exit-code based and portable.
  not_after=$(printf '%s\n' "$cert" | openssl x509 -noout -enddate 2>/dev/null \
    | sed 's/^notAfter=//')

  if ! printf '%s\n' "$cert" | openssl x509 -noout -checkend 0 >/dev/null 2>&1; then
    report="${report}CRITICAL  ${host}  EXPIRED  (notAfter=${not_after})"$'\n'
    [ "$worst" -lt 2 ] && worst=2
  elif ! printf '%s\n' "$cert" | openssl x509 -noout -checkend "$WARN_SECS" >/dev/null 2>&1; then
    report="${report}WARN      ${host}  expires within ${WARN_DAYS}d  (notAfter=${not_after})"$'\n'
    [ "$worst" -lt 1 ] && worst=1
  else
    report="${report}OK        ${host}  (notAfter=${not_after})"$'\n'
  fi
done

printf '%s' "$report"
exit "$worst"
