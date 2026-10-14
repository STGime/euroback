<!--
REVIEWER NOTES — read before publication (v2)

  1. NEW document. Terms §8 previously said an SLA "will be published at
     /legal/sla as those tiers launch" — Free and Pro are now generally
     available (out of beta), so this publishes that SLA. Team / Legal
     Team remain in closed beta and are covered only by best-effort
     until they formalise.
  2. Uptime figure is deliberately conservative: 99.5% monthly, which
     sits inside Scaleway's published Managed Database SLA range
     (standalone 99.5%; HA primary+standby 99.95%). Do NOT advertise a
     higher number without confirming it operationally first.
  3. NO financial service credits are offered yet (remediation is
     best-effort). This is intentional for the first SLA; credits can
     be introduced later, mirroring Scaleway's model (10/25/50%).
     Do not add credits here without a commercial decision.
  4. Backups: Scaleway takes automatic daily (~24h) backups of the
     managed database at the infrastructure level on every tier, so
     worst-case data loss is ~24h. These are NOT customer-controlled on
     Free/Pro. Self-serve restore from daily backups is a Team-tier
     (dedicated instance) feature. Keep this honest and do not publish
     a finer recovery-point guarantee than we can operationally meet.
  5. §6 (cessation of operations, 90-day online + export window) is a
     real continuity commitment — confirm it is consistent with the
     ISMS business-continuity handover before publication.
  6. Entity strings (Eurobase OÜ, Ahtri 12, Tallinn) mirror the rest of
     the v2 set.
-->

# Service Level Agreement

**Version 1.0 — effective 13 October 2026**

This Service Level Agreement ("**SLA**") describes the availability, backup, and support commitments Eurobase OÜ ("**Eurobase**", "we") makes for the Eurobase service ("**Service**"). It is incorporated into the **Terms of Service** at /legal/terms by reference. Capitalised terms not defined here have the meaning given in the Terms.

All Service infrastructure runs in the European Union (France) on Scaleway. No US cloud provider touches Customer Data.

## 1. Scope

- This SLA's **availability target and priority support apply to the Pro tier**, for Projects in good standing (account not suspended, invoices paid).
- The **Free tier** is provided on a **best-effort** basis with no availability target.
- **Team and Legal Team** are in closed beta; while in beta they are covered by best-effort support and by this SLA's backup and continuity sections, but not yet by the availability target or any formal credit scheme. They will come under the availability target as they reach general availability.

## 2. Availability commitment

We target **99.5% monthly availability** of the Eurobase API (authentication, database, storage, and edge-function request handling) for the Pro tier.

- "**Available**" means the API accepts and correctly serves requests for a Project. A month is a calendar month (UTC).
- Availability is measured at our platform edge and excludes the time attributable to the exclusions in §5.
- This target is underpinned by, and cannot exceed, the infrastructure commitments in §3.

## 3. Infrastructure foundation

The Service is built on Scaleway's EU infrastructure, which carries its own published SLAs:

| Component | Scaleway SLA (monthly) |
|---|---|
| Managed Database (PostgreSQL) | 99.95% High-Availability (primary + standby) / 99.5% standalone |
| Object Storage (Multi-AZ Standard) | 99.9% |

We set our availability target conservatively within these figures; we do not promise more availability than our underlying providers guarantee.

## 4. Backups and recovery

- **Automatic daily backups (all tiers).** Scaleway takes automatic backups of the managed database approximately **every 24 hours** at the infrastructure level. In a worst-case recovery, this means **up to ~24 hours of data could be lost** (recovery point objective ≈ 24h). These backups are operated by us/Scaleway for disaster recovery; they are **not customer-controlled** on Free or Pro.
- **No self-serve restore or downloadable backup on Free/Pro** (shared infrastructure).
- **Team / Legal Team** Projects run on a **dedicated** EU Postgres instance with managed **daily backups** and **self-serve restore** from those backups (Team: 7-day retention; Legal Team: 30-day), one included restore per month.
- **Data portability on every tier.** You can export your schema, all table data, and a manifest of your storage objects at any time from the console or API (see /legal/privacy, Article 20). This is how you hold your own copy; it is independent of our disaster-recovery backups.

## 5. Exclusions

The availability target does not apply to unavailability caused by:

- **Scheduled maintenance**, announced in advance via the console;
- **Emergency security patches**, which may be deployed without prior notice;
- Factors outside our reasonable control, including upstream provider outages beyond the Scaleway SLAs, network failures outside our infrastructure, and force majeure;
- Your own actions or configuration (e.g. exhausting a plan limit, invalid queries, suspended account);
- Use of the Free tier or of beta features.

## 6. Business continuity and cessation of operations

- **Key-person continuity.** Our information-security management system documents a business-continuity handover plan to keep the Service running on founder incapacitation. We publish a summary of our security practices on request.
- **If Eurobase ceases business operations**, we commit to keeping the **Service and your Customer Data online and exportable for at least 90 days** from the date we announce cessation, before any deletion. We will notify Project owners by email and keep export tooling available throughout that window so you can retrieve your data and migrate.

This is in addition to the account-termination export window in the Terms (§9).

## 7. Support

- Support is provided by email at **contact@eurobase.app**.
- Paid tiers receive **priority response**. We do not yet commit to a fixed response-time target; one will be published here as our support tiers formalise.

## 8. Remediation

Our commitment under this SLA is **best-effort remediation** — we work to restore service and prevent recurrence. We do **not** offer financial service credits at this time. If we introduce a credit scheme, it will be published in a future version of this SLA.

## 9. Changes to this SLA

We may update this SLA as the Service and its tiers evolve. Material reductions in a commitment will be announced via the console with reasonable notice. The current version is always at /legal/sla.

## 10. Contact

Questions about this SLA: **contact@eurobase.app**.

Eurobase OÜ · Ahtri 12, Tallinn 15551, Estonia · Registry code 17557586
