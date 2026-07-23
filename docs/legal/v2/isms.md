# Eurobase Information Security Management System (ISMS-lite)

**Version:** 2.0
**Effective:** 22 July 2026
**Owner:** Eurobase OÜ, Ahtri 12, Tallinn 15551, Estonia (Estonian commercial-register code 17557586)
**Review cadence:** annual (next review: **1 August 2027**)
**Contact:** security@eurobase.app

---

## 1. Purpose and scope

This document describes the Information Security Management System (ISMS) that governs how Eurobase OÜ ("Eurobase", "we") designs, operates, and evolves the Eurobase Backend-as-a-Service platform. It is written as an *ISMS-lite* — a proportionate control set for a small EU-incorporated software vendor whose customers rely on the platform for regulated workloads.

The ISMS covers:

- The Eurobase platform (production and staging), operated on Scaleway (fr-par).
- The customer data processed by that platform.
- Source code, secrets, and deploy pipelines (GitHub, Scaleway Container Registry, Kubernetes).
- Corporate systems used by Eurobase staff (email, calendar, chat, task tracking).

It is aligned to the risk-management measures in **NIS2 Directive (EU) 2022/2555, Article 21**, and the security-by-design and default requirements of **GDPR Article 32**. It does not claim ISO 27001 or SOC 2 certification (both are on the roadmap for the Team-tier launch).

The document is versioned. The current version and its checksum are published at [`/security`](https://www.eurobase.app/security) and stored alongside our other public legal documents at `docs/legal/v2/`.

---

## 2. Governance

**Accountable person:** the founder (Stefan Gimeson) as long as Eurobase operates below the NIS2 size threshold (< 50 employees AND < €10M turnover). At that threshold, a dedicated Information Security Officer will be appointed and this document reissued as ISMS v3.

**Board approval:** the sole board member of Eurobase OÜ has approved this document. Version 2.0 was signed off on the effective date above.

**Review triggers:**

- Annual review, on or before the anniversary of the effective date.
- Ad-hoc review after any *significant security incident* as defined in §5.
- Ad-hoc review upon any material change to the platform (new sub-processor, new sector/customer segment, new hosting region, staff growth crossing the NIS2 threshold).

---

## 3. Risk management

### 3.1 Risk-assessment approach

Eurobase maintains a written risk register at `docs/security/risk-register.md` (internal). Risks are recorded with:

- Asset affected (data category, system component, or process).
- Threat + vulnerability description.
- Inherent likelihood × impact score (1-5 each; product ≥ 15 = high).
- Control(s) in place.
- Residual likelihood × impact score.
- Owner and next-review date.

The register is reviewed at each ISMS review and after any incident.

### 3.2 Top identified risks (public summary)

The full register is internal; the top-tier residual risks summarised for customer transparency:

1. **Sub-processor concentration** (Scaleway). Mitigated by (a) an active technical viability check of the Scaleway Object Storage → S3 migration path, and (b) the standard Scaleway SLAs. Residual: acceptable given the sovereignty trade-off.
2. **Solo-founder key-person risk** for operations, incident response, and legal decisions. Mitigated by written runbooks, credential escrow via 1Password shared vault, and a documented business-continuity handover procedure to E-Residency Hub OÜ (our contact person) covering a 90-day window in the event of founder incapacitation.
3. **Zero-day vulnerabilities in Postgres, Deno, or Go standard libraries.** Mitigated by automated dependency updates, weekly review of security advisories, and staged rollouts to staging before production.

---

## 4. Access control and cryptography

### 4.1 Access control

- **Least privilege by design.** The platform uses six distinct Postgres roles (`eurobase_gateway`, `eurobase_developer`, `eurobase_migrator`, `eurobase_function_runner`, per-tenant `<schema>_func`, per-tenant `<schema>_ddl`) with narrowly scoped grants. Runtime exploit of the gateway pod does not grant DDL on `public.*` or cross-tenant read access. Documented in `CLAUDE.md`.
- **Superadmin flag** on platform accounts is granted only by direct database write; never by any signup, invite, or self-service flow.
- **Personal Access Tokens (PATs)** for the platform API expire on a user-configured date, are one-way hashed at rest, and are shown to the user exactly once at creation.
- **Session-level auth** uses HS256-signed JWTs with a per-user subject and a 24-hour default expiry. Refresh flow rotates the token on each use.

### 4.2 Multi-factor authentication

- **Tenant end-users** can use any of six OAuth providers, three of which (Google, GitHub, Microsoft) enforce 2FA on the provider side when the tenant end-user has 2FA enabled with that provider.
- **Platform users** (Eurobase console operators, including customers who manage projects) currently authenticate with email + password only. **Platform TOTP + WebAuthn are on the immediate roadmap** for the Team tier (target: Q4 2026) as a hard prerequisite for enterprise contracts.

### 4.3 Cryptography

| Surface | Algorithm | Notes |
|-|-|-|
| Data in transit — all HTTP endpoints | TLS 1.3 (fallback 1.2) | Managed by Scaleway load balancers. HSTS with 1-year max-age. |
| Data in transit — SDK ↔ platform | TLS 1.3 | Same as above. |
| Data at rest — Postgres | AES-256 (Scaleway managed) | Scaleway RDB default disk encryption. |
| Data at rest — S3-compatible object storage | AES-256 (SSE-S3) | Scaleway managed. |
| Vault secrets | AES-256-GCM with per-tenant key | Application-layer envelope encryption; key rotated per major release. |
| Platform JWT signing | HMAC-SHA256 | Secret in Kubernetes Secret; not in code, not in logs. |
| Webhook signing | HMAC-SHA256 | Per-webhook secret; signature includes timestamp for replay protection. |
| Unsubscribe token signing | HMAC-SHA256 | Domain-separated from JWT secret via SHA-256 derivation. |
| Password storage | bcrypt cost 12 | Applied to platform + tenant end-user passwords alike. |

Key material never appears in logs. Ephemeral key material in memory is not held for longer than the request lifetime.

---

## 5. Incident response

### 5.1 Definitions

- **Security event** — any deviation from expected system behaviour with potential security relevance.
- **Security incident** — a confirmed event that has (or is reasonably likely to have) adversely affected confidentiality, integrity, or availability of customer data or platform services.
- **Significant incident** — an incident that meets the NIS2 Article 23(3) thresholds (severe operational disruption, financial loss, or affects other natural or legal persons) or the GDPR Article 33 breach-notification thresholds.

### 5.2 Response SLAs

Aligned with **NIS2 Article 23** and **GDPR Article 33**:

| Milestone | Timeline (from awareness) | Contents |
|-|-|-|
| Internal early warning | 6 hours | Note in the incident log; on-call founder acknowledges. |
| Customer early warning | 24 hours | Indication that a significant incident has occurred, if any customer-facing impact is suspected. |
| Formal incident notification | 72 hours | Notification to affected customers with initial assessment, indicative severity, and known-facts summary. |
| Regulatory notification (data breach) | 72 hours | To the Estonian Data Protection Inspectorate (Andmekaitse Inspektsioon) when personal data confidentiality/integrity/availability is affected and the risk threshold is met. |
| CSIRT notification (NIS2 significant incident) | 24 hours early warning + 72 hours notification | To Estonian RIA / CERT-EE, applicable once Eurobase crosses the NIS2 size threshold. |
| Final report | 1 month | Root cause, corrective actions, lessons learned. Published as an anonymised post-mortem on `eurobase.app/blog/` if the impact was material. |

The Eurobase platform ships an in-console **data-breach register** that generates and enforces these SLAs on the customer side for their downstream Article 33 obligations. The same register is used internally for our own incidents.

### 5.3 Runbook

Internal runbooks at `docs/security/runbooks/`. Public entry points:

- Report a suspected incident to **security@eurobase.app**.
- Publicly disclose a vulnerability via the Coordinated Vulnerability Disclosure policy (§8).

---

## 6. Supplier and supply-chain security

### 6.1 Sub-processor register

Every third party that processes customer data on our behalf is enumerated in the public sub-processor list at `docs/legal/v2/sub-processors.md`, mirrored into the platform's Compliance tab and surfaced in every project's auto-generated RoPA (Record of Processing Activities).

Each entry carries the region of processing, the corporate parent's jurisdiction, and a flag for CLOUD Act / FISA §702 exposure. Adding a new sub-processor requires (a) a 30-day advance notice to customers per our DPA, (b) a supplier-security review captured in the risk register, and (c) an update to the RoPA generator.

### 6.2 Onboarding checklist for new suppliers

Before signing a contract:

1. Confirm the supplier's corporate parent jurisdiction.
2. Confirm data-processing region.
3. Review the supplier's SOC 2, ISO 27001, or equivalent attestation (or their published security page if smaller).
4. Sign a DPA aligned with Article 28 GDPR.
5. Record in the risk register with residual risk assessment.

### 6.3 Current suppliers with material data access

The current shortlist (in scope of §6.1, at effective date):

- **Scaleway SAS** (France) — managed Postgres, S3-compatible object storage, Kubernetes, Container Registry, Transactional Email, load balancing.
- **GatewayAPI ApS** (Denmark) — SMS delivery for phone-based OTP auth.
- **Mollie B.V.** (Netherlands) — payment processing (activation deferred to public-beta open + billing enablement).

None of the above are US-headquartered or US-owned. Full list in the sub-processor register.

---

## 7. Business continuity

### 7.1 Recovery targets

| Scenario | RTO (recovery time objective) | RPO (recovery point objective) |
|-|-|-|
| Single-tenant data corruption | 4 hours (from PITR snapshot) | 5 minutes |
| Platform-wide gateway outage | 30 minutes (Kubernetes rolling restart / rollback) | 0 minutes (no data loss — DB unaffected) |
| Full Scaleway region loss (fr-par) | 4 hours | 24 hours (last cross-region backup) |
| Founder incapacitation | 90 days | n/a (handover to contact person under 1Password credential escrow) |

The single-region worst case is the currently-highest residual risk in §3.2 and is being actively addressed by the fr-par → nl-ams evaluation. Update expected end of Q4 2026.

### 7.2 Backup regime

- **Postgres**: Scaleway automated backups at 1-hour granularity, retained 7 days.
- **Point-in-time recovery**: within any 7-day window at 1-minute resolution.
- **Cross-region backup**: nightly encrypted snapshot to nl-ams (Scaleway Amsterdam). RPO 24 hours.
- **Object storage**: Scaleway Object Storage cross-zone replication within fr-par (default).
- **Code + build artefacts**: GitHub retention + Scaleway Container Registry; not required for RTO but supports rollback.

### 7.3 Testing

- Quarterly disaster-recovery drill against a fresh Neon-style branch of the platform database. First formal drill: 15 October 2026.
- Post-drill report attached to the ISMS review.

---

## 8. Vulnerability management

### 8.1 Development-time

- **Dependency scanning** on every push (GitHub Dependabot for Go, npm, and container image bases).
- **Static analysis** via `go vet`, `golangci-lint`, `vue-tsc --noEmit`, and TypeScript strict.
- **Code review** for every merge to main via GitHub PR + mandatory approval. Substantial PRs additionally go through a cloud multi-agent security review before auto-merge.
- **Secret detection** in CI blocks pushes that would land credentials in source.

### 8.2 Runtime

- **Container image** builds daily from upstream base and rolls into staging within 24 hours.
- **Kubernetes network policies** restrict pod-to-pod traffic to the defined service graph.
- **Audit log** captures every administrative action against a project with actor, IP, and timestamp; retained per project's plan.

### 8.3 Coordinated Vulnerability Disclosure (CVD)

Reachable at **security@eurobase.app**. Full policy published at [`/security`](https://www.eurobase.app/security).

Handling SLA:

- **Acknowledgement** of receipt: within 3 business days.
- **Initial assessment**: within 10 business days.
- **Fix or documented mitigation**: within 90 days of confirmation for high/critical; within 180 days for medium; low or informational at Eurobase's discretion.
- **Public disclosure**: coordinated with the reporter; no legal action against good-faith researchers who follow the policy.

We do not currently operate a paid bug-bounty programme; we publicly credit reporters on request.

---

## 9. Human-resources security

Applicable when Eurobase employs staff beyond the founder:

- **Written employment contracts** including confidentiality provisions.
- **Onboarding checklist** covering ISMS awareness, credential provisioning, MFA setup.
- **Off-boarding checklist** covering credential revocation, device return, exit interview.
- **Access review** quarterly and on role change.

Until then, this section formalises what applies to the founder as sole operator.

---

## 10. Compliance controls mapping

This ISMS-lite is designed to satisfy the following external requirements without claiming certification:

- **GDPR Article 32** — appropriate technical and organisational measures — satisfied by §§4, 5, 8.
- **GDPR Article 33** — data-breach notification — satisfied by §5.2.
- **NIS2 Directive (EU) 2022/2555, Article 21** — cybersecurity risk-management measures — mapped in the `/security` page (public control matrix).
- **NIS2 Article 23** — incident-reporting SLAs — satisfied by §5.2 (applies when Eurobase crosses the size threshold).
- **ISO 27001 Annex A** — used as a reference framework for control coverage; formal certification is a Team-tier roadmap item.

---

## 11. Document control

- **Location:** `docs/legal/v2/isms.md` in the Eurobase repository (public).
- **Approver:** Stefan Gimeson, sole board member of Eurobase OÜ.
- **Review interval:** annual, plus event-triggered.
- **Version history:** git log at the repository.
- **Public checksum:** available at `/security#isms` on the marketing site.
