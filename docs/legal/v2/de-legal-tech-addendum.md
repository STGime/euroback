<!--
REVIEWER NOTES — read before publication

  1. This addendum extends the DPA v2 with two paragraphs specific to
     German legal-tech customers (Rechtsanwaltskanzleien, Steuerberater,
     legal-ops): §43e BRAO cloud engagement + §203 StGB
     Verschwiegenheitsverpflichtung.
  2. It is BUNDLED with the Legal Team tier (M2b) — plain-Team customers
     get the DPA v2 alone; Legal-Team customers get the DPA + this
     addendum + a downloadable Verschwiegenheitsverpflichtung template
     they can attach to their own client contracts.
  3. Governed by Estonian law (inherited from Terms §16). The German
     paragraph references are what the CUSTOMER must comply with under
     their own Berufsordnung; this addendum is our commitment to shape
     our processing so the customer CAN comply.
  4. Lawyer review required — specifically an Estonian lawyer AND a
     German lawyer who has handled §203 StGB cloud engagements before.
     Do not publish without both sign-offs.
-->

# Legal Team addendum — German legal-tech compliance

**Effective 2 October 2026. Applies to Customers on the Legal Team tier only.**

This addendum extends the Data Processing Agreement (DPA v2) with commitments specific to German legal-tech, Steuerberater, and legal-ops customers whose own regulatory framework — the Bundesrechtsanwaltsordnung (BRAO), the Berufsordnung für Rechtsanwälte (BORA), the Strafgesetzbuch §203 (Berufsgeheimnis), the Abgabenordnung (AO), and the Handelsgesetzbuch (HGB) — imposes obligations that flow through to their processors.

## 1. §43e BRAO — engagement of the SaaS provider

Eurobase acknowledges that when a German-registered attorney (Rechtsanwalt) or attorney firm engages Eurobase as an IT services provider under §43e BRAO ("Inanspruchnahme von Dienstleistungen"), the following commitments apply for the duration of the engagement:

1. **Written contract.** This addendum, the DPA v2, and the Terms of Service jointly constitute the written contract §43e BRAO Abs. 2 Nr. 2 requires.
2. **Confidentiality obligation on the provider.** Eurobase and its personnel are bound to secrecy about all Customer data as defined in §2 below.
3. **Right of the attorney to control and audit.** The Customer may — on 14 days' written notice, no more than twice per calendar year — request audit access to Eurobase's technical and organisational measures relevant to their data. Audit fees are borne by the Customer unless the audit uncovers a material breach.
4. **Sub-processor chain.** Sub-processors are listed in Annex 3 of the DPA v2. Eurobase will notify the Customer at least 30 days before adding or replacing a sub-processor that processes attorney data. The Customer may object; on unresolved objection, the Customer may terminate for cause.
5. **EU-only processing.** All infrastructure is hosted in the EU (Scaleway, France). No data leaves the EU. See the Sub-processor list for verification.

## 2. §203 StGB — Verschwiegenheitsverpflichtung of Eurobase personnel

Under §203 (3) StGB (as amended by the 2017 Neuregelung des Schutzes von Berufsgeheimnissen bei der Mitwirkung Dritter), a lawyer may use a SaaS only if the processor's personnel with access to attorney data are individually bound to §203-equivalent secrecy. Eurobase commits to the following:

1. **Individual written declarations.** Every Eurobase employee, contractor, and freelancer whose role includes potential access to attorney data signs a Verschwiegenheitsverpflichtung acknowledging the criminal-liability provisions of §203 StGB. A template of the declaration is available from the Customer's account manager on request.
2. **Register.** The current set of signed declarations is queryable by the Customer at any time via a Console endpoint (`GET /platform/projects/{id}/compliance/staff-secrecy-register`) or by direct request to the account manager. The register records name, role, signature date, and a SHA-256 hash of the signed PDF text.
3. **Revocation on offboarding.** When a person leaves Eurobase or moves to a role without data access, their declaration is marked revoked in the register (the row is kept in place to preserve the compliance timeline; access is disabled via the standard offboarding process).
4. **Sub-processor cascade.** Sub-processors handling attorney data must, per Eurobase's contract with them, extend the §203-equivalent obligation to their own personnel. Sub-processor compliance is reviewed annually.

## 3. Interaction with other Customer obligations

This addendum enables the Customer's compliance; it does not perform it on the Customer's behalf. The Customer remains responsible for:

- Their own §43e BRAO documentation (Vertrag, Auftraggeberliste, Verfahrensdokumentation).
- Their client contracts, which must include the notice that a §203-bound processor is engaged.
- Any additional Berufskammer requirements specific to their region or practice area.

## 4. Termination

Termination of the Legal Team tier terminates this addendum but not the underlying DPA. Customers who downgrade to plain Team continue under the DPA v2 alone.

## 5. Precedence

Where this addendum conflicts with the DPA v2 or the Terms of Service, this addendum controls for Legal Team customers.

---

**Eurobase OÜ, Ahtri 12, Tallinn 15551**
Signatory: DPO — dpo@eurobase.app
