<!--
REVIEWER NOTES — read before publication

  1. Scope: this DPA covers personal data Customers process VIA Eurobase
     about THEIR end-users — Eurobase as processor, Customer as
     controller (Art. 28 GDPR). Personal data we collect about the
     Customer themselves (their email, billing, etc.) is in the
     Privacy Policy at /legal/privacy.
  2. This DPA is incorporated by reference into the Terms of Service
     and accepted by click-wrap at signup. Acceptance must be recorded
     (legal_acceptances table — Phase 2).
  3. The clause referencing the live compliance report at
     /legal/compliance/<project_id> assumes that endpoint is reachable
     by Customer. Verify the route is wired before publication.
  4. Annex 2 (TOMs) lists concrete controls in the codebase. Keep in
     sync with reality at version bumps.
  5. Annex 3 (Sub-processors) is rendered live from the sub_processors
     DB table; the static text below is a snapshot for completeness.
  6. Lawyer review required.
-->

# Data Processing Agreement

**Version 2.0 — effective {{EFFECTIVE_DATE}}**

This Data Processing Agreement ("**DPA**") is entered into between **{{LEGAL_ENTITY}}**, registered at **{{REGISTERED_ADDRESS}}** ("**Eurobase**", "**Processor**"), and the customer identified in the Eurobase account ("**Customer**", "**Controller**"). It is incorporated by reference into the Terms of Service at /legal/terms and applies whenever Customer uses the Service to process personal data about its end-users.

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
- Database backups managed by Scaleway, point-in-time recovery available.

**Availability and resilience**
- Managed PostgreSQL with automated failover (when Customer enables HA).
- Kubernetes Kapsule cluster with multi-node redundancy and auto-healing.
- Periodic backup restore tests.

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

The current list, with country, role, security certifications, and a link to each provider's own DPA, is published at **/legal/sub-processors**. As of {{EFFECTIVE_DATE}}:

| Sub-processor | Country | Role | Certs |
|---|---|---|---|
| Scaleway SAS | France | Hosting, managed PostgreSQL, object storage, transactional email, Kubernetes | ISO 27001, HDS, SecNumCloud (where applicable) |
| GatewayAPI (OnlineCity ApS) | Denmark | SMS for phone authentication (when Customer enables it) | ISO 27001 |
| Mollie B.V. | Netherlands | Payment processing (when paid plans are active) | PCI DSS Level 1 |
| Google LLC | United States | Google OAuth (when Customer enables it for its own end-users) | EU-US DPF, ISO 27001, SOC 2 |
| GitHub, Inc. (Microsoft) | United States | GitHub OAuth (when Customer enables it for its own end-users) | EU-US DPF, SOC 2 |

Eurobase will give Customer at least 30 days' notice before adding or replacing any of these.

## Annex 4 — Contact points

**Eurobase data protection contact:** dpo@eurobase.app
**Customer data protection contact:** as set in the Customer's Project settings; defaults to the Project owner's email.
