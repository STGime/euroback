#!/usr/bin/env bash
# Read-only check of the functions pods' egress rules
# (deploy/k8s/functions-networkpolicy.yaml), on one Running functions pod:
#   - SMTP port 25 must be BLOCKED (functions-deny-smtp),
#   - submission port 2525 must be OPEN (a function may send through the
#     customer's own provider),
#   - HTTPS must be OPEN (functions-egress allows the public internet),
#   - Cilium must enforce the port-25 deny on that pod's endpoint, with
#     ingress still not enforced (the policy mustn't block invocations).
# While Scaleway's default security group also blocks 25 / 465 / 587 on
# the nodes, a timeout on 25 proves nothing about our policy — hence the
# endpoint check. Opens TCP connections and closes them; sends nothing.
#
# Usage: ./scripts/ops/functions-egress-probe.sh
set -euo pipefail

NS=eurobase
SMTP_HOST=${SMTP_HOST:-smtp-relay.brevo.com}
HTTPS_HOST=${HTTPS_HOST:-www.scaleway.com}

pod=$(kubectl -n "$NS" get pods -l app=functions --field-selector=status.phase=Running \
  -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
if [[ -z $pod ]]; then echo "no Running functions pod"; exit 1; fi
node=$(kubectl -n "$NS" get pod "$pod" -o jsonpath='{.spec.nodeName}')
podip=$(kubectl -n "$NS" get pod "$pod" -o jsonpath='{.status.podIP}')
echo "pod $pod ($podip) on $node"

probe() {
  local host=$1 port=$2
  kubectl -n "$NS" exec "$pod" -c deno-runner -- deno eval --quiet "
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
echo "HTTPS $HTTPS_HOST:443 → $r (want OPEN)"
[[ $r == OPEN ]] || fail=1

# The policy as enforced on that pod's endpoint (read-only).
agent=$(kubectl -n kube-system get pods -l k8s-app=cilium --field-selector "spec.nodeName=$node" \
  -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
if [[ -z $agent ]]; then
  echo "no Cilium agent found on $node"; fail=1
else
  cil() { kubectl -n kube-system exec "$agent" -c cilium-agent -- cilium-dbg "$@"; }
  if ! eps=$(cil endpoint list 2>&1); then
    echo "cilium-dbg endpoint list failed: $eps"; fail=1
  else
    line=$(grep -F " $podip " <<<"$eps" | head -1 || true)
    epid=$(awk '{print $1}' <<<"$line")
    if [[ -z $epid ]]; then
      echo "no Cilium endpoint for $podip"; fail=1
    else
      if grep -q "Disabled" <<<"$(awk '{print $2, $3}' <<<"$line")"; then
        echo "endpoint $epid: ingress enforcement Disabled (invocations unaffected)"
      else
        echo "endpoint $epid: ingress enforcement is ON — check invocations"; fail=1
      fi
      if ! bpf=$(cil bpf policy get "$epid" 2>&1); then
        echo "cilium-dbg bpf policy get failed: $bpf"; fail=1
      elif grep -E "Deny +Egress" <<<"$bpf" | grep -q "25/TCP"; then
        echo "endpoint $epid: Deny Egress 25/TCP enforced"
      else
        echo "endpoint $epid: NO Deny Egress 25/TCP entry"; fail=1
      fi
    fi
  fi
fi

if [[ $fail == 0 ]]; then echo "OK: port 25 blocked, submission and HTTPS open"; else echo "UNEXPECTED: see above"; exit 1; fi
