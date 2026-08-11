# BSI C5 roadmap

**Status: in planning. Target Type-1 attestation: 12 months post the Team-tier public launch.**

The BSI C5 (Cloud Computing Compliance Criteria Catalogue, current edition C5:2020) is the Bundesamt für Sicherheit in der Informationstechnik's cloud-security criteria catalogue. It is the German public-sector default for cloud-service vendor evaluation and increasingly appears in RFPs from regulated private-sector customers (legal-tech, health-tech, fintech).

Eurobase is not yet C5-attested. This document sets out the roadmap.

## Position today

- **Not attested.** Neither Type 1 nor Type 2.
- **Controls in place.** Approximately 60% of the C5:2020 basic-criteria control set is already implemented in the platform — audit logging (with hash-chain integrity), encryption in transit and at rest, EU-only sub-processors, breach notification workflow, DSAR support, staff secrecy declarations, dedicated database per Team-tier project. See the DPA v2 Annex 2 (TOMs) for the concrete list.
- **Gaps.** Formal documentation of change-management processes, incident-response drills, and physical security (delegated to Scaleway, whose own BSI C5 attestation covers the physical layer — we will link that on the Trust page). Also: penetration-testing cadence and an ISMS with periodic management review.

## Planned path

- **Phase 1 — Gap analysis (Q1 2027).** External assessor performs a Basis-Kriterien gap analysis. Expected effort: 2–3 weeks. Output: prioritised remediation list.
- **Phase 2 — Remediation (Q2 2027).** Close documented gaps. Formal ISMS. Publish security policies.
- **Phase 3 — Type 1 attestation (Q3 2027).** Auditor confirms the control design is adequate at a point in time. Expected effort: 4–6 weeks. Output: signed Type 1 report.
- **Phase 4 — Type 2 attestation (Q3 2028).** Auditor confirms the controls operated effectively over a 12-month observation window. Ongoing.

## What Customers can rely on today, in advance of attestation

- Sub-processors are themselves BSI C5-attested where their scope is material (Scaleway).
- Every technical control listed in DPA v2 Annex 2 is implementable and verifiable by the Customer against the running platform.
- Sign-off on this roadmap is written into every Legal Team engagement — a slip on the target dates is a material breach the Customer may cite.

## Contact

Questions or a request to see the interim gap-analysis output when Phase 1 completes: **dpo@eurobase.app**.
