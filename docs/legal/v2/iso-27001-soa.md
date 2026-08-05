# ISO 27001 — Statement of Applicability (draft)

**Status: in planning. Certification target: 12–18 months post the Team-tier public launch.**

This document is a working-draft Statement of Applicability (SoA) for ISO/IEC 27001:2022 as applied to Eurobase's platform. It is neither a certificate nor a substitute for one — a certified copy will be published on the Trust page once the certification audit is complete.

## Scope

The Eurobase Platform (gateway, worker, functions runner, console) hosted in EU (Scaleway, fr-par).

## Applicability of Annex A controls

Every control in ISO 27001:2022 Annex A has been assessed for applicability. Full detail is available under NDA (contact dpo@eurobase.app); a high-level summary follows.

| Domain | Control count | Applicable | Implemented | In progress |
|---|---:|---:|---:|---:|
| A.5 — Organisational | 37 | 35 | 24 | 11 |
| A.6 — People | 8 | 8 | 6 | 2 |
| A.7 — Physical | 14 | 3 | 3 (all delegated to Scaleway) | 0 |
| A.8 — Technological | 34 | 34 | 26 | 8 |

Two Annex A controls are not applicable and this is documented:

- **A.5.29 — Information security during disruption.** Currently declared not applicable because Eurobase has no dedicated disaster-recovery site distinct from the production Scaleway region. This will become applicable when the Team-tier expansion into a second EU region (roadmap 2027) lands.
- **A.7.4 — Physical security monitoring.** Not applicable at the Eurobase organisational level (fully delegated to Scaleway's data-centre operator). The delegation is documented in the sub-processor register.

## Path to certification

- **Q1 2027** — Complete SoA finalisation with an external assessor. Publish redacted version.
- **Q2 2027** — Internal audit.
- **Q3 2027** — Management review.
- **Q4 2027** — Stage 1 audit (documentation review).
- **Q1 2028** — Stage 2 audit (implementation review). Target: certificate issued.

## Contact

For the current full SoA under NDA, or to request a draft of specific Annex A controls: **dpo@eurobase.app**.
