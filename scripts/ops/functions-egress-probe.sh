#!/usr/bin/env bash
# Read-only check of the functions pods' egress rules
# (deploy/k8s/functions-networkpolicy.yaml): from inside a running
# functions pod, try a TCP connection to
#   - SMTP port 25 (must be BLOCKED: functions-deny-smtp),
#   - submission port 2525 (must be OPEN: a function may send through the
#     customer's own provider), and
#   - HTTPS (must be OPEN: functions-egress allows the public internet).
# While Scaleway's default security group also blocks 25 / 465 / 587 on
# the nodes, a timeout on 25 proves nothing about our policy — so the
# script also checks that Cilium on the pod's node has it loaded.
# Opens TCP connections and closes them; sends nothing.
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
      .catch((e) => 'ERROR (' + e.name + ')');
    console.log(await Promise.race([c, t]));
    Deno.exit(0);
  " 2>/dev/null || echo "probe failed"
}

# A Cilium deny drops the SYN silently: only a timeout is BLOCKED. A
# refused / reset connection means the packet left the cluster (so the
# policy didn't stop it); a DNS error means nothing was tested.
fail=0
r=$(probe "$SMTP_HOST" 25)
echo "SMTP  $SMTP_HOST:25    → $r (want BLOCKED (timeout))"
[[ $r == "BLOCKED (timeout)" ]] || fail=1
r=$(probe "$SMTP_HOST" 2525)
echo "SMTP  $SMTP_HOST:2525  → $r (want OPEN)"
[[ $r == OPEN ]] || fail=1
r=$(probe "$HTTPS_HOST" 443)
echo "HTTPS $HTTPS_HOST:443 → $r"
[[ $r == OPEN ]] || fail=1

# The policy itself, as loaded by Cilium on the pod's node (read-only).
pod=$(kubectl -n "$NS" get pods -l app=functions -o jsonpath='{.items[0].metadata.name}')
node=$(kubectl -n "$NS" get pod "$pod" -o jsonpath='{.spec.nodeName}')
agent=$(kubectl -n kube-system get pods -l k8s-app=cilium --field-selector "spec.nodeName=$node" -o jsonpath='{.items[0].metadata.name}')
if kubectl -n kube-system exec "$agent" -c cilium-agent -- cilium-dbg policy get 2>/dev/null | grep -q functions-deny-smtp; then
  echo "Cilium on $node has functions-deny-smtp loaded"
else
  echo "Cilium on $node does NOT list functions-deny-smtp"; fail=1
fi

if [[ $fail == 0 ]]; then echo "OK: port 25 blocked, submission and HTTPS open"; else echo "UNEXPECTED: see above"; exit 1; fi
