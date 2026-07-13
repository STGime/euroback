<!--
REVIEWER NOTES — read before publication

  1. This is a STATIC SNAPSHOT for the docs folder. The live page on
     the marketing/console site should render this list dynamically
     from the sub_processors DB table so it cannot drift from reality.
     The static text below mirrors what the seed migrations produce
     today.
  2. Whenever a new sub-processor is added or replaced:
       - Insert into sub_processors in the same migration that adds
         the integration (CLAUDE.md: Compliance / Sub-Processors flow);
       - Bump this static doc;
       - Email project owners 30 days before activation
         (notify_subprocessor_changes flag — Phase 2).
  3. CLOUD-Act flag is set by the registry; we frame this neutrally:
     "based outside the EU" + "transfer mechanism: EU-US DPF".
     Per user preference: do NOT use anti-US wording.
-->

# Sub-Processors

**Version 2.0 — effective {{EFFECTIVE_DATE}}**

This page lists every third party that processes personal data on behalf of Eurobase customers. We keep it short and EU-first by design.

If you are a customer, the **Data Processing Agreement** at /legal/dpa gives you the right to object to a new sub-processor on reasonable data-protection grounds during the 30-day notice period before they begin processing.

To get an email when this list changes, enable **Subscribe to sub-processor updates** in your account settings. (Account owners are subscribed by default once that feature ships.)

## Always-on sub-processors

These are required for the platform to function for any customer.

### Scaleway SAS — France 🇫🇷

- **Role:** Hosting (Kubernetes Kapsule), managed PostgreSQL, transactional email (TEM), object storage (S3-compatible), managed Redis.
- **Data location:** France (Paris region, DC-PAR1 / DC-PAR2).
- **Certifications:** ISO 27001, ISO 27017, ISO 27018, HDS, SecNumCloud (where applicable).
- **DPA:** https://www.scaleway.com/en/data-protection-agreement/
- **Privacy:** https://www.scaleway.com/en/privacy-policy/

## Conditional sub-processors

These only process data when you enable the corresponding feature on your Project.

### GatewayAPI (OnlineCity ApS) — Denmark 🇩🇰

- **Role:** Transactional SMS for phone-based authentication.
- **Activated when:** you enable phone sign-in on a Project.
- **Data shared:** end-user phone number (E.164) and a one-time verification code (delivered, not stored long-term).
- **Certifications:** ISO 27001.
- **DPA:** https://gatewayapi.com/legal/dpa/
- **Privacy:** https://gatewayapi.com/legal/privacy/

### Mollie B.V. — Netherlands 🇳🇱

- **Role:** Payment processing.
- **Activated when:** you subscribe to a paid plan (paid plans are not yet active).
- **Data shared:** billing contact email and identifier; payment-card and bank-transfer data are collected directly by Mollie and never reach Eurobase.
- **Certifications:** PCI DSS Level 1.
- **DPA:** https://www.mollie.com/en/legal/data-processing-agreement
- **Privacy:** https://www.mollie.com/privacy

### Google LLC — United States 🇺🇸

- **Role:** "Sign in with Google" for end-users of *your* application.
- **Activated when:** you enable Google OAuth on a Project.
- **Data shared:** end-user's Google identifier, email, and basic profile fields you choose to request.
- **Transfer mechanism:** EU-US Data Privacy Framework.
- **Certifications:** ISO 27001, ISO 27017, ISO 27018, SOC 2.
- **DPA:** https://cloud.google.com/terms/data-processing-addendum
- **Privacy:** https://policies.google.com/privacy

### GitHub, Inc. (Microsoft) — United States 🇺🇸

- **Role:** "Sign in with GitHub" for end-users of *your* application.
- **Activated when:** you enable GitHub OAuth on a Project.
- **Data shared:** end-user's GitHub identifier, email, and username.
- **Transfer mechanism:** EU-US Data Privacy Framework.
- **Certifications:** SOC 2.
- **DPA:** https://docs.github.com/en/site-policy/privacy-policies/github-data-protection-agreement
- **Privacy:** https://docs.github.com/en/site-policy/privacy-policies/github-general-privacy-statement

## How we add or replace sub-processors

1. We update the `sub_processors` table in the same change that introduces the integration.
2. We add it to this page with an "active from" date at least 30 days in the future.
3. We email account owners (and anyone subscribed to updates).
4. During the notice period, you can object on reasonable data-protection grounds. If we cannot accommodate the objection we will let you terminate the affected service for convenience and refund the unused portion of any prepaid fees.

## Changelog

- **v1.0 ({{EFFECTIVE_DATE}})** — Initial publication: Scaleway, GatewayAPI, Mollie, Google, GitHub.
