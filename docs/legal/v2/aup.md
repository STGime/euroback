<!--
REVIEWER NOTES — read before publication

  1. AUP is referenced from Terms §6 and is a contractual obligation.
     Keep it short and enforceable; we'll get into trouble for
     overpromising more than for underpromising.
  2. Reporting flow at the bottom commits us to act on abuse reports.
     Make sure abuse@eurobase.app actually receives mail before
     publication (currently aliased to founder).
  3. CSAM clause: legally required to cooperate with hotlines (INHOPE,
     national reporters). Do NOT remove without lawyer review.
  4. Pre-incorporation: replace {{LEGAL_ENTITY}} once registered.
-->

# Acceptable Use Policy

**Version 2.0 — effective {{EFFECTIVE_DATE}}**

This Acceptable Use Policy ("**AUP**") sets out what you may not do with the Eurobase service. It is incorporated into the **Terms of Service** at /legal/terms. Breach of this AUP is breach of those Terms, and may lead to suspension, termination, and reporting to law enforcement.

You are responsible for everything that happens under your account, including content put on the platform by your end-users. Build the safeguards your application needs.

## 1. You must not host, transmit, or process

- **Illegal content** under EU law or the law of any jurisdiction your users are in.
- **Child sexual abuse material (CSAM).** Discovery is reported to the relevant national hotline and to law enforcement, with affected accounts terminated immediately and preserved for investigation.
- **Material that incites violence, terrorism, or genocide**, or that promotes the violent overthrow of democratic institutions.
- **Material that violates the rights of others** — copyright, trademark, trade secrets, privacy, publicity, or any other intellectual or proprietary right — without authorisation.
- **Defamatory or harassing content** targeted at identifiable individuals.
- **Doxing** — publication of personal data with the intent to harass, intimidate, or facilitate offline harm.
- **Non-consensual intimate imagery** of any kind.

## 2. You must not use the platform to

- **Send spam.** Bulk, unsolicited, or non-transactional email and SMS, including fake-confirmation flows, mailing-list scraping, and address-harvesting. Eurobase transactional email is for your own users who asked for it.
- **Phish or mislead.** Set up sites that impersonate other organisations to extract credentials, payment data, or personal information.
- **Run malware** — host, distribute, command, control, or stage malicious code (viruses, worms, ransomware, RATs, info-stealers, etc.).
- **Attack other systems** — port scanning, brute force, credential stuffing, exploitation, DDoS staging, or any other unauthorised access attempt against systems you do not own.
- **Mine cryptocurrencies** in any form (CPU, GPU, Edge Function, browser cryptojacking).
- **Circumvent rate limits, plan limits, billing controls, or abuse detection** through multiple accounts, proxies, or technical workarounds.
- **Run gambling, gaming for stakes, or financial services** unless you hold the licence the activity requires in every jurisdiction it touches, and have shown that licence to us on request.

## 3. Resource use

You may not deliberately impose disproportionate load on the platform — for example, by scheduling unbounded recursive calls, infinite loops in Edge Functions, or by storing data engineered to slow down indexing. Eurobase reserves the right to throttle Projects whose usage threatens platform stability for other customers, and to bill (or terminate) Projects whose usage is grossly disproportionate to their plan.

## 4. End-user trust and safety

If your application has end-users:

- Have a way for them to report abuse to you, and a way for them to delete their account and data (Eurobase provides API endpoints for this — see DPA Annex 2).
- Comply with the GDPR as **controller** of their data; we are processor (see DPA at /legal/dpa).
- Do not subject them to dark patterns that prevent withdrawal of consent or account deletion.

## 5. Security

- Do not deliberately probe, scan, or test the security of any Eurobase system or any other customer's Project. Responsible vulnerability research on your own Project is welcome — please report findings to **security@eurobase.app**; see /legal/security for the full disclosure programme (forthcoming).
- Keep your API keys, Personal Access Tokens, and other secrets confidential. Rotate them if you suspect compromise.
- Do not share your account credentials. Each human user must have their own login.

## 6. Compliance with sanctions and export control

You may not use Eurobase from, or to provide services to, any individual or entity that is the target of EU, UN, UK, or US sanctions. You confirm that no person with controlling interest in your account is so listed.

## 7. Reporting abuse

If you see content or behaviour on Eurobase that violates this AUP, please email **abuse@eurobase.app** with: a description, the URL or Project reference, and any evidence (screenshots, log excerpts). We act on reports within **2 business days** for routine matters and immediately for CSAM, active malware distribution, or imminent harm.

We may publish anonymised statistics about reports and actions. We do not disclose the identity of reporters except as required by law.

## 8. Enforcement

Where this AUP is breached, we may, depending on the severity:

- Send a warning and ask you to fix the issue within a stated time;
- Restrict the affected feature (e.g. block outbound email);
- Suspend the Project or the account;
- Terminate the contract under the Terms of Service;
- Report to law enforcement and preserve relevant data under legal hold.

We give notice where reasonably practical. We do not give notice where doing so would frustrate the action (for example, in active CSAM, fraud, or attack scenarios).

## 9. Changes

We may update this AUP from time to time. Material changes are announced 30 days before they take effect, in line with the Terms of Service.

## 10. Contact

- General abuse reports: **abuse@eurobase.app**
- Security vulnerability reports: **security@eurobase.app**
- Data protection: **dpo@eurobase.app**
- Legal notices: **{{NOTICES_EMAIL}}**
