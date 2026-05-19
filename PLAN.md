# CVE-2026-46333 ("ssh-keysign-pwn") Mitigation Plan

## Threat

Linux kernel logical flaw in process management allows Local Privilege Escalation + Information Disclosure. An attacker in a pod can use unprivileged process tracing (ptrace) to extract host-level secrets (SSH private keys, `/etc/shadow`), leading to full host compromise and container escape.

**Public exploits are available.** High criticality for Eurobase — the functions-runner executes untrusted tenant JS on shared Kapsule nodes.

## Mitigation

Block unprivileged process tracing by setting `kernel.yama.ptrace_scope=3` on every node. This is the Scaleway-recommended workaround until patched kernel images are rolled.

`ptrace_scope=3` means "no process may trace any other process" — the most restrictive setting. Neither the Go gateway, the Deno functions runner, pgx, nor any Eurobase component uses ptrace, so this has zero functional impact.

## Implementation (mirrors the Dirty Frag / Copy Fail pattern)

### Step 1: DaemonSet — `deploy/k8s/security/disable-ptrace.yaml`

```yaml
# CVE-2026-46333 ("ssh-keysign-pwn") mitigation.
# Sets kernel.yama.ptrace_scope=3 on every Kapsule node to block
# unprivileged process tracing. Same DaemonSet pattern as
# disable-algif-aead.yaml and disable-dirty-frag.yaml.
```

DaemonSet spec:
- **namespace:** `kube-system`
- **tolerations:** all taints (covers autoscaler-spawned nodes + functions-pool if PR #46 lands)
- **initContainer:** `alpine:3.23@sha256:<pin-digest>` (privileged, same digest as disable-dirty-frag)
  - `sysctl -w kernel.yama.ptrace_scope=3`
  - Verify with `cat /proc/sys/kernel/yama/ptrace_scope`
  - Write `/etc/sysctl.d/99-disable-ptrace.conf` so the setting survives a sysctl reload (but not a reboot — the DaemonSet re-applies on boot)
- **main container:** `registry.k8s.io/pause:3.10` (1m CPU / 8Mi memory)
- **hostPID: true** — required for sysctl to affect the host kernel

### Step 2: CI wiring — `.github/workflows/ci.yml`

Add `kubectl apply -f deploy/k8s/security/disable-ptrace.yaml` in the security apply block (alongside the existing algif-aead and dirty-frag lines), before any pod restart.

Add `kubectl rollout status daemonset/disable-ptrace -n kube-system --timeout=120s` in the verify step.

### Step 3: Verify

- `kubectl get ds -n kube-system disable-ptrace` → DESIRED == CURRENT == READY
- On any node: `cat /proc/sys/kernel/yama/ptrace_scope` → `3`

### Step 4: Follow-up — patched kernel roll

Once Scaleway publishes the patched Kapsule node image:

```bash
scw k8s pool upgrade <pool-id>
```

The DaemonSet stays in place as defense-in-depth (same as the other two).

## Risk check

No Eurobase component uses ptrace:
- Go gateway/worker: no debugger, no strace
- Deno functions runner: V8 doesn't ptrace
- pgx, gorilla/websocket: no ptrace
- `kubectl exec` into pods still works (it uses the CRI API, not ptrace)

The only thing broken by `ptrace_scope=3` is attaching `strace`/`gdb` to a running process on the node for live debugging. If needed during an incident, the operator can temporarily `sysctl -w kernel.yama.ptrace_scope=1` on the specific node.

## Rollback

```bash
kubectl delete -f deploy/k8s/security/disable-ptrace.yaml
# Then on each node (if needed immediately):
sysctl -w kernel.yama.ptrace_scope=1
```

## Files

| File | Change |
|------|--------|
| `deploy/k8s/security/disable-ptrace.yaml` | **NEW** — DaemonSet |
| `.github/workflows/ci.yml` | Add apply + verify lines |
