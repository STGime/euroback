#!/usr/bin/env bash
# Read-only check of the functions pods' egress rules
# (deploy/k8s/functions-networkpolicy.yaml): from inside a running
# functions pod, try a TCP connection to
#   - an SMTP submission port (must be BLOCKED: functions-deny-smtp), and
#   - HTTPS (must be OPEN: functions-egress allows the public internet).
# Port 2525 is the meaningful SMTP probe: Scaleway's default security
# group blocks 25/465/587 on the nodes anyway, 2525 only our policy does.
# Opens a TCP connection and closes it; sends nothing.
#
# Usage: ./scripts/ops/functions-egress-probe.sh
set -euo pipefail

NS=eurobase
SMTP_HOST=${SMTP_HOST:-smtp-relay.brevo.com}
HTTPS_HOST=${HTTPS_HOST:-www.scaleway.com}

probe() {
  local host=$1 port=$2
  kubectl -n "$NS" exec deploy/functions -c deno-runner -- deno eval --quiet "
    const t = new Promise((r) => setTimeout(() => r('BLOCKED (timeout)'), 5000));
    const c = Deno.connect({ hostname: '$host', port: $port })
      .then((conn) => { conn.close(); return 'OPEN'; })
      .catch((e) => 'BLOCKED (' + e.name + ')');
    console.log(await Promise.race([c, t]));
    Deno.exit(0);
  " 2>/dev/null || echo "probe failed"
}

fail=0
for port in 2525 587 465 25; do
  r=$(probe "$SMTP_HOST" "$port")
  echo "SMTP  $SMTP_HOST:$port  → $r"
  [[ $r == BLOCKED* ]] || fail=1
done
r=$(probe "$HTTPS_HOST" 443)
echo "HTTPS $HTTPS_HOST:443 → $r"
[[ $r == OPEN ]] || fail=1

if [[ $fail == 0 ]]; then echo "OK: mail ports blocked, HTTPS open"; else echo "UNEXPECTED: see above"; exit 1; fi
