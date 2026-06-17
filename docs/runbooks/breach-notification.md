<!--
INTERNAL RUNBOOK — not customer-facing.

This runbook is referenced from the DPA (Section 10) and the Privacy
Policy (Section 7). It commits Eurobase to:
  - 24h target to notify Customers (controllers).
  - 72h target to notify the lead supervisory authority.
  - Notify affected end-users without undue delay where Art. 34 applies.

Keep this runbook short and operational. Detail goes in linked
templates and dashboards, not here.
-->

# Personal-Data Breach Notification Runbook

**Owner:** DPO (dpo@eurobase.app)
**Audience:** on-call engineers, founders, anyone who first sees an incident.

A personal-data breach is **any** breach of security leading to accidental or unlawful destruction, loss, alteration, unauthorised disclosure of, or access to, personal data (GDPR Art. 4(12)). It does not require malicious intent — a misconfigured S3 bucket counts.

When in doubt, treat it as a breach. The cost of a false positive is one extra runbook execution; the cost of a false negative is regulatory liability and customer trust.

---

## T+0 — first hour: contain and triage

1. **Page the DPO** at dpo@eurobase.app and the on-call engineer.
2. **Open an incident channel** (`#incident-YYYYMMDD-<short-name>`) and start a written timeline. Every action gets a timestamped entry. Do not investigate verbally; we will need the record later.
3. **Scope the blast radius**. What data, whose data, how many records, what time window, what attacker capability is implied?
4. **Stop the bleeding** — revoke compromised tokens, rotate credentials, block the offending IP, take the misconfigured surface offline. Do this *before* root-causing.
5. **Preserve evidence** — snapshot logs, DB state, S3 keys, audit trail. Do not delete anything until the DPO clears it.

If the incident is clearly bounded (e.g. a single user's session token leaked to themselves through a UI bug, no data exposed) the DPO may de-escalate after triage.

## T+0 to T+24h — Customer notification (DPA §10)

We commit to notifying affected **Customers** within 24 hours of becoming aware. "Becoming aware" = the moment any Eurobase staff member has reasonable certainty that a breach has occurred — *not* the moment the investigation is complete.

The notification email contains, to the best of current knowledge:

- Nature of the breach.
- Categories of data affected and approximate number of records.
- Categories of data subjects and approximate number.
- Likely consequences.
- Measures taken or proposed to address the breach and to mitigate its effects.
- A point of contact (DPO email) for follow-up.

Use the template at `docs/runbooks/templates/breach-customer-email.md`. Dispatch it via the breach register endpoint (`POST /platform/projects/{id}/compliance/breaches/{incidentId}/notify-customers`) so the send is recorded on the append-only register and a corresponding `audit_log` entry is written for hash-chain verification. Customers may have their own end-user notification obligations under Art. 34; we do not notify their end-users on their behalf.

## T+0 to T+72h — Supervisory authority notification (Art. 33)

If the breach has any non-trivial impact on Customer Data, the DPO files the Art. 33 notification with the **CNIL** (Commission nationale de l'informatique et des libertés — France, our establishment) within 72 hours of becoming aware. Filing portal: **https://notifications.cnil.fr/notifications/index** (login with the DPO's CNIL account; if locked out, the DPO's recovery contact is the founder).

Render the structured paste-in for the filing via `POST /platform/projects/{id}/compliance/breaches/{incidentId}/authority-form` and copy section-by-section into the portal. Use the authority's standard form; partial information is acceptable if a follow-up is filed promptly.

For incidents affecting end-users in another Member State, the CNIL acts as the lead SA under the one-stop-shop mechanism (Art. 56) and forwards as needed. Do not file with multiple SAs unless explicitly instructed.

If a notification cannot be made within 72 hours, document the reason for the delay in writing.

For breaches that are *not likely to result in a risk* to data subjects, we still log internally but do not notify the supervisory authority. The DPO makes that judgement call and writes it down.

## When the breach is high-risk to data subjects (Art. 34)

If the breach is **likely to result in a high risk to the rights and freedoms** of data subjects (e.g. plaintext passwords, financial data, location data, health data), we also notify affected end-users directly. For data where Eurobase is processor (tenant end-users), we coordinate with the Customer; the Customer is the controller and decides on end-user comms — but we provide them with everything they need within the 24-hour window above.

Where Eurobase is controller (platform users — see Privacy Policy for scope), we send a direct notice from **dpo@eurobase.app** without the Customer in the loop.

## After the immediate window — root cause, fix, learn

- **Root cause analysis** within 5 working days. Written, blameless, focused on systemic causes (process, defaults, monitoring) not individuals.
- **Permanent fix** scoped and tracked. Quick fixes from T+0 stay in place until the permanent fix lands.
- **Public post-mortem** for material incidents, on /security or in a customer email. Default to transparency; redact only what is genuinely sensitive (active attacker indicators of compromise, ongoing investigation detail).
- **Update this runbook** with anything that surprised us during the response.

## Records

The DPO maintains a register of all personal-data breaches under Art. 33(5) — including those not notified to the authority. The register lives in **`public.breach_register`** (migration 000065) and is exposed read/write via `/platform/projects/{id}/compliance/breaches`. The table is **append-only**: every state change writes a NEW row keyed by `incident_id`; the most recent row by `seq` is the authoritative snapshot. UPDATE/DELETE are revoked from the runtime roles (same pattern as `audit_log` in 000058), and the WORM object archive (issue #171, shipping alongside) provides off-box defence against a compromised gateway forging fresh appends.

Each entry records:

- Date of awareness, date and time of breach (or window).
- Description of the breach.
- Categories and approximate numbers (data subjects, records).
- Likely consequences.
- Measures taken.
- Whether notified to the supervisory authority and Customers; if not, why.

We keep the register indefinitely.

## Pre-flight checklist before publication of /legal/dpa and /legal/privacy

- [ ] dpo@eurobase.app receives mail (test from outside the org).
- [ ] abuse@eurobase.app receives mail.
- [ ] security@eurobase.app receives mail.
- [ ] On-call rotation set up; everyone in the rotation has read this runbook. Primary on-call: founder. Backup: DPO. Rotation lives in the team password manager (1Password vault "On-Call").
- [ ] Customer-email and authority-form templates written and saved alongside this file (`docs/runbooks/templates/breach-customer-email.md`, `docs/runbooks/templates/breach-authority-form.md`).
- [ ] CNIL portal URL confirmed and DPO account verified (login test once per quarter).
- [ ] `breach_register` table provisioned (migration 000065 applied in production); `eurobase_gateway` retains only SELECT + INSERT.
- [ ] `DPO_EMAIL` env var set on the gateway Deployment (defaults to `dpo@eurobase.app`).
- [ ] Synthetic breach drill executed end-to-end against a seeded tenant; MTTD/MTTR metrics observed on the Grafana board.

When all of the above are checked, the 24-hour DPA SLA and the 72-hour Art. 33 SLA are ones we can actually meet.
