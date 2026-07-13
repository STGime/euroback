<!--
REVIEWER NOTES — read before publication

  1. Scope: this Privacy Policy covers personal data Eurobase processes
     AS CONTROLLER — i.e. data about the developers/customers using the
     Eurobase platform itself. Personal data Customers process via the
     platform on behalf of their own end-users is governed by the DPA
     (/legal/dpa), where Eurobase is processor.
  2. Pre-incorporation. Replace every {{PLACEHOLDER}} once the operating
     entity is registered. Do NOT publish with placeholders left in.
  3. The retention table cites concrete code paths so engineering can
     verify the policy matches reality at publication time. Re-grep
     the codebase before publishing.
  4. Mandatory before publication:
       - Right-to-erasure handler (Phase 2: gdpr_erasure.go).
       - Account-delete UI (Phase 2).
       - Token cleanup job (Phase 2: cleanup_expired_tokens.go).
       - Audit-log retention job (Phase 2: cleanup_audit_log.go).
       - DPO mailbox dpo@eurobase.app actually receiving mail.
       - Lawyer review.
-->

# Privacy Policy

**Version 1.0 — effective {{EFFECTIVE_DATE}}**

This policy explains how **{{LEGAL_ENTITY}}** ("**Eurobase**", "**we**") handles personal data of people who use the Eurobase platform — visitors to our website, sign-ups, account holders, billing contacts, and people who write to us. If you are an end-user of an application *built on* Eurobase, your data is controlled by that application's operator, not by us; please contact them. (Technical detail of how their data flows through Eurobase is in the **Data Processing Agreement** at /legal/dpa.)

## 1. Who is the controller

**{{LEGAL_ENTITY}}**
{{REGISTERED_ADDRESS}}
Registry: {{REGISTRY_NUMBER}}
VAT: {{VAT_NUMBER}}
Email: **{{CONTACT_EMAIL}}**
Data protection contact: **dpo@eurobase.app**

You can reach our data protection contact for any question about this policy or to exercise your rights (Section 6).

## 2. What we collect, why, and how long we keep it

We collect only the minimum needed to operate the Service.

| Category | Data | Source | Purpose | Lawful basis (GDPR Art. 6) | Retention |
|---|---|---|---|---|---|
| Identity | email, display name | You, at signup | Identify your account, contact you | (b) Contract | Until you delete your account |
| Authentication | password hash (bcrypt), email confirmation timestamp, last sign-in time | You + system | Log you in, secure your account | (b) Contract | Until you delete your account |
| Billing reference | Mollie customer ID, plan tier | Mollie + system | Manage subscription and invoices | (b) Contract; (c) Legal obligation (bookkeeping) | 10 years from the last invoice (statutory accounting period) |
| Console activity logs | source IP, user-agent, request method/path, status, latency | Your browser/CLI | Debugging, abuse prevention, security | (f) Legitimate interest (running a secure service) | 1, 7, or 30 days depending on your plan; auto-purged hourly |
| Audit log | actor email, source IP, action taken | System | Evidence of administrative changes | (f) Legitimate interest; (c) Legal obligation | 24 months |
| API keys, Personal Access Tokens | secure hash, prefix, last-used time | You + system | Authenticate CLI/MCP/CI access | (b) Contract | Until you revoke the key, or 30 days after expiry |
| Email tokens | hash, type (confirmation, password-reset, magic-link) | System | Verify email, reset password, sign in | (b) Contract | Until expiry, then auto-purged daily |
| Waitlist | email | You | Sequence early access | (a) Consent | Until you sign up, or 24 months from waitlist date |
| Allowlist | email | You / inviter | Gate access during early access | (b) Contract / (f) Legitimate interest | Until removed |
| Project invitations | invitee email, expiry | You | Add team members | (b) Contract | 7 days from creation |
| Support correspondence | email, content | You | Answer your question | (b) Contract; (f) Legitimate interest | 24 months |

We do **not** collect: precise location, special-category data (health, religion, etc.), payment-card numbers (Mollie holds those), advertising or behavioural-tracking data.

## 3. Cookies and similar technologies

The Eurobase console does not set HTTP cookies. We use a small number of `localStorage` keys for authentication and developer-tool state; full detail is in the **Cookie & Storage Notice** at **/legal/cookies**. We do not use marketing or analytics cookies. No consent banner is required today; if that ever changes we will disclose it on the cookie page and ask for your consent.

## 4. Who sees your data (recipients & sub-processors)

We rely on a short list of EU-based service providers to run the platform: Scaleway (hosting, database, email, object storage — France), GatewayAPI (SMS — Denmark), and (when we activate paid plans) Mollie (payments — Netherlands). We may also share data with professional advisers (legal, accounting) under confidentiality, and with public authorities where required by law.

If you choose to enable Google or GitHub social sign-in for *your* application, those providers receive end-user identifiers — they do not receive your platform-account data unless you separately sign in to the Eurobase console with them.

The current list, with each provider's role, certifications, and DPA, is published at **/legal/sub-processors** and is updated whenever it changes. We give 30 days' notice before any new sub-processor starts processing data; you can object during that period by terminating the affected service.

## 5. International transfers

Your platform-account data stays in the European Union (France) under our default configuration. Eurobase does not export account data to non-EU countries. The two exceptions are sub-processors that you optionally enable for OAuth on your *own* applications (Google, GitHub — established in the United States); transfers in those cases rely on the EU-US Data Privacy Framework and on the providers' supplementary safeguards. You can keep your application EU-only by leaving those OAuth providers disabled.

## 6. Your rights

Under the GDPR you have the right to:

- **Access** — get a copy of the personal data we hold about you. Use the in-app "Download my data" button or email **dpo@eurobase.app**. We respond within one month.
- **Rectification** — correct inaccurate data. Most fields are editable in the console; for anything else, email us.
- **Erasure** — have your data deleted. Use the in-app "Delete account" button or email **dpo@eurobase.app**. Some data must be retained for legal reasons (e.g. invoices) — we will tell you which.
- **Restriction** — ask us to pause processing while a dispute is resolved.
- **Portability** — receive your data in a structured, commonly used, machine-readable format (we provide JSON export).
- **Object** — object to processing based on our legitimate interests; we will stop unless we have compelling legitimate grounds that override your interests.
- **Withdraw consent** — where processing is based on consent (e.g. waitlist), withdraw at any time. This does not affect prior lawful processing.
- **Not be subject to automated decisions** — we do not make automated decisions with legal or similarly significant effects about you.
- **Lodge a complaint** with a supervisory authority. Our lead authority is **{{LEAD_SA}}** ({{LEAD_SA_URL}}). You may also complain to the authority in the EU country where you live.

We do not charge for these requests unless they are manifestly unfounded or excessive.

## 7. Security

- Passwords are stored as bcrypt hashes with a cost factor of 12.
- All traffic to the platform is over TLS 1.2+.
- Data at rest in the database is encrypted by Scaleway managed PostgreSQL.
- Tenant data is isolated by PostgreSQL Row-Level Security and per-project schemas.
- The runtime database role (`eurobase_gateway`) has no DDL rights; migrations run as a separate restricted role (`eurobase_migrator`).
- We follow Scaleway security advisories closely and have a documented vulnerability response process; recent example: kernel CVE-2026-31431 was mitigated cluster-wide within hours of disclosure.

We notify affected users without undue delay where a personal-data breach is likely to result in a high risk to their rights and freedoms (Art. 34 GDPR). For platform-wide incidents we also notify the lead supervisory authority within 72 hours (Art. 33).

## 8. Children

The Service is not directed at children under 16. We require a 16+ confirmation at signup for consumer accounts. If you believe a child has given us personal data, please contact **dpo@eurobase.app** and we will delete it.

## 9. Changes to this policy

If we change this policy in a material way we will post the new version at **/legal/privacy**, update the version number and effective date, and email account holders at least 30 days before the change takes effect. Earlier versions are archived at **/legal/changelog**.

## 10. How to contact us

For any privacy question or to exercise your rights:

**Email:** dpo@eurobase.app
**Postal:** {{LEGAL_ENTITY}}, {{REGISTERED_ADDRESS}}

We respond in writing within one calendar month. Where the request is complex we may extend by two further months and we will tell you why.
