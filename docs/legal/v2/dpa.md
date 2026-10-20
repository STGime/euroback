<!--
REVIEWER NOTES — read before publication (v2)

  1. Scope: this DPA covers personal data Customers process VIA Eurobase
     about THEIR end-users — Eurobase as processor, Customer as
     controller (Art. 28 GDPR). Personal data we collect about the
     Customer themselves (their email, billing, etc.) is in the
     Privacy Policy at /legal/privacy.
  2. What changed vs v1:
       - Click-through acceptance is now recorded (legal_acceptances
         table, migration 000074, landed with PR #279). Closes the
         Phase 2 gap the v1 header called out.
       - Governing law is inherited from the Terms (§16 — Republic
         of Estonia + Harju County Court, Tallinn). No text change
         to this DPA's §13 needed because it already references
         "the law applicable to the Terms of Service."
  3. What changed vs v2.0 (this v2.1 amendment):
       - Annex 3 restructured into two tables — CORE sub-processors
         (always active) and FEATURE-CONDITIONAL sub-processors
         (active only when Customer enables the corresponding
         feature). Customer inquiry (Sg-Prenden-Lanke 1993 e.V.,
         2026-09-17) surfaced that the flat single-table format
         reads as if every processor always applies, causing
         auditor confusion for customers who have not enabled
         optional providers.
       - Apple, Microsoft (Entra ID), and LinkedIn added to the
         feature-conditional table. These OAuth providers are
         already offered in the console auth-provider settings
         but were previously missing from Annex 3 — Article 28(2)(a)
         requires prior authorisation for each sub-processor, so
         they must be listed even when feature-conditional.
       - No changes to §§1-14 wording; the annexes are the only
         diff. Existing acceptance carries forward per the
         30-day notice + no-objection convention documented in
         terms.md v2.1 §12.
  4. This DPA is incorporated by reference into the Terms of Service
     and accepted by click-wrap at signup.
  5. The clause referencing the live compliance report at
     /console/projects/{id}/compliance assumes that endpoint is
     reachable by Customer. Verify the route is wired before
     publication.
  6. Annex 2 (TOMs) lists concrete controls in the codebase. Keep in
     sync with reality at version bumps.
  7. Annex 3 (Sub-processors) is rendered live from the sub_processors
     DB table; the static text below is a snapshot for completeness.
     The registry.go feature-detection is already the source of truth
     for which conditional processors are active for a specific
     project.
  8. Lawyer review required.
  9. Ops: before this file's effective date, send the 30-day change
     notice to all existing customers per Annex 3 line about "30
     days' notice before adding or replacing." Apple / Microsoft /
     LinkedIn are the additions that trigger the notice window.
-->

# Data Processing Agreement

**Version 2.1 — effective 17 October 2026**

This Data Processing Agreement ("**DPA**") is entered into between **Eurobase OÜ**, registered at **Ahtri 12, Tallinn 15551** ("**Eurobase**", "**Processor**"), and the customer identified in the Eurobase account ("**Customer**", "**Controller**"). It is incorporated by reference into the Terms of Service at /legal/terms and applies whenever Customer uses the Service to process personal data about its end-users.

By creating a Project on Eurobase you accept this DPA. The Eurobase representative authorised to sign physical counterparts on request is the person identified at **dpo@eurobase.app**.

## 1. Subject matter and duration

Eurobase processes personal data for the Customer for as long as the Customer's account is active, plus any post-termination period set out in the Terms of Service.

## 2. Nature and purpose of processing

To run the Eurobase Backend-as-a-Service: storing, retrieving, and serving the data the Customer chooses to put into Projects, including managing end-user identities, hosting application files, sending transactional email and SMS, and providing operational logs and analytics to the Customer.

## 3. Categories of personal data and data subjects

**Data subjects** — end-users of Customer's applications, and any third parties whose data the Customer chooses to upload (for example, contacts in an address book the Customer's app stores).

**Categories of personal data** — those described in Annex 1 below, plus any further data the Customer chooses to store in its Project. The current snapshot is also visible to the Customer in the live compliance report at **/console/projects/{id}/compliance**, which lists the categories enabled by the features the Customer has activated.

## 4. Customer obligations as Controller

Customer warrants that it has a lawful basis under GDPR Art. 6 (and, where relevant, Art. 9) for the personal data it puts into the Service, and that any consents required have been obtained. Customer is responsible for the lawfulness of the data, the accuracy of any retention configuration it sets, and for issuing instructions to Eurobase via the Service interface.

## 5. Eurobase obligations as Processor (Art. 28(3))

Eurobase will:

(a) **Process only on documented instructions.** Customer's documented instructions are these Terms, the DPA, and any configuration the Customer makes through the Service. If we believe an instruction breaches GDPR or other EU/Member-State data protection law, we will tell the Customer.

(b) **Confidentiality.** Anyone we authorise to access personal data is under a written confidentiality obligation.

(c) **Security.** Implement and maintain the technical and organisational measures in **Annex 2**. We may update them, but only in ways that maintain or improve the level of security.

(d) **Sub-processors.** Engage sub-processors only under the conditions in Section 7 below.

(e) **Assist the Controller.** Help Customer respond to data-subject requests (Art. 12–22), and meet its security, breach-notification, DPIA, and prior-consultation duties (Art. 32–36), insofar as the nature of the processing and the information available to us allow.

(f) **Return or delete.** At end of services, delete or (at Customer's choice) return all personal data, except where EU/Member-State law requires us to retain it.

(g) **Make audit information available.** Provide Customer with information necessary to demonstrate compliance with this Section 5 and allow for audits (Section 9).

## 6. Data-subject requests

If a data subject contacts Eurobase directly, we will (a) not respond to the substance of the request, (b) tell the data subject to contact the Customer, and (c) inform the Customer within **5 business days**. Eurobase provides API endpoints to help Customer service Article 15 (access) and Article 17 (erasure) requests; the documentation is in **Annex 2**.

## 7. Sub-processors

Customer gives Eurobase **general written authorisation** to engage sub-processors. The current list is at **/legal/sub-processors**. We will:

- Impose data protection obligations on sub-processors that are no less protective than this DPA (Art. 28(4));
- Notify Customer at least **30 days** before adding or replacing a sub-processor (by email and on the page above);
- Allow Customer to **object on reasonable data-protection grounds** during the notice period. If we cannot accommodate the objection, Customer may terminate the affected service for convenience and receive a pro-rata refund of prepaid fees.

We remain fully liable to Customer for the acts and omissions of our sub-processors as if they were our own (Art. 28(4) last sentence).

## 8. International transfers

Eurobase processes Customer Data in the European Union by default. The only routine non-EU transfer is when Customer enables a US-based OAuth provider (Google, GitHub) for its own end-users; in that case the transfer relies on the **EU-US Data Privacy Framework** and on the supplementary measures the providers publish. Customer can keep its Project EU-only by leaving those OAuth providers disabled.

If we ever propose a non-EU transfer outside this scope, we will use a transfer mechanism approved under Chapter V GDPR (e.g. SCCs) and notify Customer in advance under Section 7.

## 9. Audits

On Customer's reasonable written request, and not more than once per year, Eurobase will:

- Provide our latest TOMs documentation, security certifications, and pen-test summaries;
- Answer reasonable written questions about our compliance with this DPA;
- For Enterprise customers (where applicable), allow an on-site audit by Customer or an independent auditor under confidentiality and at Customer's expense, with at least 30 days' notice and at a time that minimises disruption.

We pre-empt some of this by making sub-processor information, the live compliance report, and the breach runbook available on demand.

## 10. Personal-data breach

If we become aware of a personal-data breach affecting Customer Data we will notify Customer **without undue delay and in any case within 24 hours**. Notice will include, to the extent then known: the nature and scope of the breach, categories and approximate number of data subjects and records, likely consequences, measures taken or proposed, and a contact for further information. Customer is responsible for any notification to its end-users and to its supervisory authority.

## 11. Liability

Liability under this DPA is governed by the limitations and carve-outs in the Terms of Service. Nothing in this DPA limits or excludes liability that cannot lawfully be limited or excluded.

## 12. Termination, return, and deletion

This DPA terminates automatically when the Terms of Service end. On termination Customer has a 30-day window to export Customer Data via the console. After that we delete production data within 30 days and backups within 90 days, except where retained under EU/Member-State law (and only for the period required).

## 13. Governing law

This DPA is governed by the law applicable to the Terms of Service.

---

## Annex 1 — Description of the processing

| Category | Personal data | Stored in | Source | Retention |
|---|---|---|---|---|
| End-user identity | email, display name, avatar URL | Per-tenant `users` table on Scaleway PostgreSQL (France) | End-user signup; Customer-controlled | Until Customer deletes or end-user erases |
| Authentication | password hash (bcrypt), email/phone confirmation timestamps, last sign-in time | Per-tenant `users` table | System | Until Customer deletes or end-user erases |
| Phone | E.164 phone number | Per-tenant `users` + `email_tokens` (during OTP) | End-user signup | Until deletion; OTPs purged on use or after 10 minutes |
| OAuth identities | provider, provider user ID, claims JSON | Per-tenant `user_identities` table | OAuth provider | Until end-user disconnects or account deleted |
| Session tokens | hashed refresh-token, expiry, revocation timestamp | Per-tenant `refresh_tokens` table | System | Until expiry or revocation; auto-purged daily |
| Email/phone tokens | hashed token, type, expiry | Per-tenant `email_tokens` table | System | Until use or expiry; auto-purged daily |
| Custom user metadata | arbitrary JSON the Customer's app writes | Per-tenant `users.metadata` JSONB | Customer's app | Customer-controlled |
| Application files | object key, MIME type, byte size, uploader user ID, metadata | Per-project bucket on Scaleway Object Storage (France) + metadata in PostgreSQL | End-user uploads | Customer-controlled |
| Encrypted secrets | application-encrypted blob | Per-tenant `vault_secrets` table | Customer-controlled | Customer-controlled |
| Request logs | source IP, user-agent, method, path, status, latency | Project log table | Gateway | 1, 7, or 30 days depending on Customer plan |

The exact subset that applies to a given Project depends on the features the Customer has activated and is reflected in the live compliance report.

## Annex 2 — Technical and organisational measures (TOMs)

**Confidentiality**
- TLS 1.2+ for all connections.
- Tenant data isolated by per-project PostgreSQL schemas and Row-Level Security policies.
- Runtime DB role has no DDL rights; migrations run under a separate restricted role.
- Object storage requires authenticated, time-limited presigned URLs by default.

**Integrity**
- Bcrypt password hashing (cost 12).
- Audit log of administrative actions in the platform console.
- Database backups managed by Scaleway, restore-from-backup and on-demand-snapshot paths available (see Recovery objectives below).

**Availability and resilience**
- Managed PostgreSQL with automated failover (when Customer enables HA).
- Kubernetes Kapsule cluster with multi-node redundancy and auto-healing.
- Periodic backup restore tests (see `docs/runbooks/backup-pitr-test.md` and the monthly automated regression at `deploy/k8s/backup-pitr-monthly-test-cronjob.yaml`).
- **Recovery objectives (measured, not aspirational).** Numbers below come from `scripts/ops/monthly-backup-pitr-test.sh` runs against a throwaway Scaleway RDB instance; procedure documented in the runbook. Same runbook, same script re-measures on the 1st of every month once the monthly CronJob's ops image lands.
    - **RTO** — measured restore time at ~5 MB dataset: **16 seconds** (fixed provisioning + plumbing overhead — dominates at small data volumes). Restore time increases with database size; for Team-tier workloads above ~100 MB, Eurobase provides a bespoke measurement on request rather than a linear extrapolation. Test executed 2026-09-06 via the customer-facing `backup create → backup restore` path.
    - **RPO** — **up to 24 hours** between the scheduled backups Scaleway RDB takes automatically. Team-tier customers can take on-demand snapshots at any point to reduce this window to a duration of their own choosing — a snapshot taken immediately before a risky migration reduces the RPO on that specific recovery to seconds. Tighter default guarantees (continuous replication to a warm standby) are a deliberate future scope-out on cost grounds, not a technical limit.

**Process**
- Vulnerability monitoring and timely patching (e.g. CVE-2026-31431 mitigated within hours of disclosure).
- Documented incident-response runbook; 24-hour breach-notification SLA to Controllers.
- Access to production restricted by SSO and audit-logged.
- Personnel sign confidentiality undertakings; access reviews quarterly (when team size warrants).

**Customer-facing endpoints to assist Art. 12–22 requests**
- `GET /platform/projects/{id}/users/{userId}/export` — Article 15 subject access export (JSON).
- `DELETE /platform/projects/{id}/users/{userId}` — Article 17 erasure (cascades through DB + object storage).
- `GET /platform/projects/{id}/compliance` — live Article 30 record for the Customer's Project.

## Annex 3 — Authorised sub-processors

The current list, with country, role, security certifications, and a link to each provider's own DPA, is published at **/legal/sub-processors**. The tables below are a snapshot as of **17 October 2026**. The live per-project view — reflecting which conditional processors are actually active for Customer's specific Project based on the features Customer has enabled — is at **/console/projects/{id}/compliance**.

### 3.1 Core sub-processors — always active

These process personal data on Customer's behalf regardless of which features Customer enables.

| Sub-processor | Country | Role | Certs |
|---|---|---|---|
| Scaleway SAS | France | Hosting, managed PostgreSQL, object storage, transactional email, Kubernetes | ISO 27001, HDS, SecNumCloud (where applicable) |

### 3.2 Feature-conditional sub-processors — active only when triggered

These sub-processors receive personal data ONLY when Customer explicitly enables the corresponding feature in its Project. A Customer that has not enabled a given feature does not share any personal data with the listed sub-processor, and the DPA has no operational effect against that entry for that Customer.

| Sub-processor | Country | Role | Triggered when | Certs |
|---|---|---|---|---|
| Mollie B.V. | Netherlands | Payment processing | Customer subscribes to a paid plan (Pro, Team, Legal Team) | PCI DSS Level 1 |
| GatewayAPI (OnlineCity ApS) | Denmark | SMS delivery for phone-OTP authentication | Customer enables the phone-OTP auth provider in Project settings | ISO 27001 |
| Google LLC | United States | Google OAuth (Customer's end-user identity federation) | Customer enables the Google OAuth provider in Project settings | EU-US DPF, ISO 27001, SOC 2 |
| GitHub, Inc. (a Microsoft company) | United States | GitHub OAuth (Customer's end-user identity federation) | Customer enables the GitHub OAuth provider in Project settings | EU-US DPF, SOC 2 |
| Apple Inc. | United States | Sign in with Apple (Customer's end-user identity federation) | Customer enables the Apple OAuth provider in Project settings | EU-US DPF, ISO 27001, SOC 2 |
| Microsoft Ireland Operations Ltd. | Ireland | Microsoft (Entra ID) OAuth (Customer's end-user identity federation) | Customer enables the Microsoft OAuth provider in Project settings | ISO 27001, EU DPF |
| LinkedIn Ireland Unlimited Company | Ireland | LinkedIn OAuth (Customer's end-user identity federation) | Customer enables the LinkedIn OAuth provider in Project settings | ISO 27001, EU DPF |

### 3.3 Change notice

Eurobase will give Customer at least 30 days' notice before adding or replacing any entry in either table. Customer may object to a new sub-processor within the notice window; if the objection cannot be resolved by removing the corresponding feature from Customer's Project, Customer may terminate the affected Project without penalty for the remainder of any prepaid billing period.

## Annex 4 — Contact points

**Eurobase data protection contact:** dpo@eurobase.app
**Customer data protection contact:** as set in the Customer's Project settings; defaults to the Project owner's email.
