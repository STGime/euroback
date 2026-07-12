<!--
REVIEWER NOTES — read before publication (v2)

  1. Estonia incorporation. {{LEGAL_ENTITY}}, {{REGISTERED_ADDRESS}},
     {{REGISTRY_NUMBER}}, {{VAT_NUMBER}}, {{EFFECTIVE_DATE}} are still
     placeholders — filled in the same commit that closes company
     formation. Governing law + jurisdiction ARE locked (Estonia +
     Harju County Court, Tallinn) — the entity choice makes them
     non-negotiable.
  2. What changed vs v1:
       - Governing law: Germany → Estonia (§16).
       - Court of jurisdiction: Berlin → Harju County Court, Tallinn (§16).
       - Section 9 gains a beta-window sentence about non-preservation
         and platform-side reset before general availability.
       - Signup flow now records click-wrap acceptance to the
         legal_acceptances table (migration 000074), closing the
         Phase 2 gap called out in the v1 header.
  3. Audience: B2B + B2C. Consumer-only clauses are tagged [CONSUMER].
     B2B-only clauses are tagged [B2B]. Untagged clauses apply to both.
  4. Any material change ships as v3 in a new folder; users are
     emailed 30 days before the change takes effect.
-->

# Terms of Service

**Version 2.0 — effective {{EFFECTIVE_DATE}}**

These Terms of Service ("**Terms**") form a contract between **{{LEGAL_ENTITY}}**, registered at **{{REGISTERED_ADDRESS}}**, registry number **{{REGISTRY_NUMBER}}**, VAT **{{VAT_NUMBER}}** ("**Eurobase**", "**we**", "**us**") and you, the person or organisation creating an account ("**you**", "**Customer**"). By creating an account or using the Service you accept these Terms. If you act on behalf of an organisation you confirm you have authority to bind it.

## 1. Definitions

- **Service** — the Eurobase Backend-as-a-Service platform, including the database, authentication, storage, edge functions, and console accessible at eurobase.app.
- **Account** — your access credentials and the data associated with them.
- **Project** — a workspace you create within the Service to host your application's data.
- **Customer Data** — anything you or your end-users upload to or create within a Project.
- **End-User** — a person who uses an application built by you on top of the Service.
- **Sub-Processor** — a third party that processes Customer Data on our behalf, listed at /legal/sub-processors.
- **DPA** — the Data Processing Agreement at /legal/dpa, which forms part of these Terms when you process personal data via the Service.
- **AUP** — the Acceptable Use Policy at /legal/aup, which forms part of these Terms.

## 2. The Service

We provide an EU-hosted backend platform: managed PostgreSQL, authentication, object storage, transactional email, edge functions, and an administrative console. All infrastructure runs in the European Union (currently France, on Scaleway). The Service is offered as it stands at any given time; new features may be added and minor features may be removed with reasonable notice.

## 3. Account creation and access

You must register an account to use the Service. During the current early-access period, account creation may be gated by waitlist or invitation. You agree to provide accurate information at signup and to keep it current. **[CONSUMER]** When signing up as a consumer, you confirm you are at least 16 years old. You are responsible for keeping your credentials confidential and for activity under your account.

## 4. Right of withdrawal **[CONSUMER]**

If you sign up as a consumer (acting outside any trade, business, craft or profession), you have the right to withdraw from the contract within **14 days** of account creation without giving any reason. To exercise the right, send an unambiguous statement to **{{WITHDRAWAL_EMAIL}}** or use the model form in **Annex A**. Effects of withdrawal: we will refund any payments received without undue delay and no later than 14 days after we are informed of your decision, using the same payment method unless you agree otherwise.

If you ask us to begin performance of the Service during the withdrawal period and you then withdraw, you owe a proportionate amount for the part of the Service performed up to the point of withdrawal. If the Service has been fully performed at your express request before the end of the withdrawal period, you lose your right of withdrawal (Art. 16(a) Directive 2011/83/EU).

## 5. Plans, pricing, and billing

The Service is currently free during early access. Paid plans will be introduced with at least 30 days' written notice. When billing is active:

- Charges are stated on the pricing page at the time of subscription. Taxes are added where applicable.
- Subscriptions renew automatically unless cancelled before the renewal date. **[CONSUMER]** Consumers may cancel a renewing subscription at any time effective at the end of the current billing period.
- Refunds are issued only as required by law or this section.
- Payments are processed by Mollie B.V. (Netherlands). We do not store full payment-card data.

## 6. Acceptable use

Your use of the Service is subject to the **AUP**. We may suspend Projects or terminate accounts that violate the AUP, with notice where reasonably practical and immediately where required to protect the Service, other customers, or third parties.

## 7. Customer Data and personal data

You retain all rights in your Customer Data. You grant us a non-exclusive licence to host, process, transmit, display, and back up Customer Data solely as necessary to provide the Service. We do not access Customer Data except as required to operate the Service, to comply with law, or with your instructions.

Where Customer Data includes personal data, you act as **controller** and we act as **processor**. The DPA at **/legal/dpa** governs that processing and is incorporated into these Terms by reference. By using the Service to process personal data you accept the DPA.

## 8. Service levels and support

During early access we provide best-effort availability and email support at **{{SUPPORT_EMAIL}}**. A formal Service Level Agreement and target uptime will be published at **/legal/sla** before paid plans launch. Planned maintenance is announced in advance via the console. Emergency security patches may be deployed without prior notice (see our incident handling at **/legal/security**).

## 9. Suspension and termination

You may close your account at any time via the console. We may suspend or terminate your account or any Project: (a) for material breach of these Terms or the AUP, (b) where required by law or court order, (c) where the Service is at risk (DDoS, exploit, etc.). We will give reasonable notice where practical.

On termination by either party we keep your Customer Data available for export for **30 days**, after which it is irreversibly deleted from production systems and within 90 days from backups. We will delete sooner on your written request, subject to legal retention requirements (e.g. accounting records).

**Beta window.** During the current public beta, we may reset, downgrade, or migrate any Project as part of platform preparation for general availability. We will give at least 14 days' notice before a reset that affects your Customer Data, and export tooling will remain available throughout that window. Nothing in the Service during beta is a commitment to preserve Customer Data indefinitely; the general-availability launch may require a fresh Project.

## 10. Intellectual property

All Service software, branding, and documentation are owned by Eurobase or its licensors and protected by copyright, trademark, and other laws. We grant you a limited, revocable, non-transferable licence to use the Service in accordance with these Terms. You retain all rights in Customer Data and in code you write that runs on the Service. Any feedback you give us may be used by us without obligation.

## 11. Warranties and disclaimers

The Service is provided **as is**. We make no warranty that the Service will be uninterrupted, error-free, or fit for any specific purpose. **[CONSUMER]** This does not affect your statutory rights as a consumer under EU law and Estonian law, including conformity guarantees under Directive (EU) 2019/770 as implemented in the Estonian Law of Obligations Act (VÕS).

## 12. Liability

**[B2B]** To the maximum extent permitted by law, our aggregate liability arising out of or in connection with these Terms is capped, per twelve-month period, at the fees you paid us in that period. We are not liable for indirect, incidental, special, consequential, or punitive damages, or for loss of profits, revenue, goodwill, or data.

**[CONSUMER]** Nothing in these Terms limits or excludes our liability for: death or personal injury caused by our negligence; fraud or fraudulent misrepresentation; gross negligence or wilful misconduct; or any liability that cannot be limited or excluded under applicable law. For all other liability, our total cumulative liability is capped at the greater of (a) the fees you paid us in the 12 months preceding the event giving rise to the claim, or (b) EUR 100.

## 13. Indemnity **[B2B]**

You will defend and indemnify Eurobase against third-party claims arising from your Customer Data, your use of the Service in breach of these Terms or the AUP, or your violation of law. We will tell you about any such claim promptly and let you control the defence (subject to your duty not to settle in a way that admits Eurobase's liability without our consent).

## 14. Confidentiality

Each party will protect the other's non-public information disclosed under these Terms with the same care it uses for its own confidential information, and will use it only to perform under these Terms.

## 15. Changes to these Terms

We may change these Terms by posting a new version at **/legal/terms** and emailing the address on your account at least **30 days** before the change takes effect. If you do not accept the change, you may close your account before the effective date and we will refund the unused portion of any prepaid fees. Continued use after the effective date is acceptance.

## 16. Governing law and jurisdiction

These Terms are governed by the law of the **Republic of Estonia**, excluding its conflict-of-laws rules. **[B2B]** The **Harju County Court (Harju Maakohus)** in Tallinn has exclusive jurisdiction. **[CONSUMER]** Consumers may bring proceedings in the courts of the EU Member State where they are habitually resident, and we may bring proceedings only in those courts. Consumers may also use the EU Online Dispute Resolution platform at **https://ec.europa.eu/consumers/odr**. We do not commit to participate in alternative dispute resolution unless required to.

## 17. Miscellaneous

- **Entire agreement.** These Terms (together with the AUP, DPA, Privacy Policy and any pricing pages) are the entire agreement between us regarding the Service and supersede prior understandings.
- **Severability.** If any clause is held unenforceable the rest stays in force.
- **Assignment.** You may not assign these Terms without our written consent. We may assign to an affiliate or in connection with a merger or sale of substantially all our assets.
- **No waiver.** Our delay in enforcing a right is not a waiver.
- **Notices.** We send notices by email to the address on your account. You send notices to **{{NOTICES_EMAIL}}**.
- **Force majeure.** Neither party is liable for delay or non-performance caused by events outside its reasonable control.

## 18. Contact

**{{LEGAL_ENTITY}}**
{{REGISTERED_ADDRESS}}
General: **{{SUPPORT_EMAIL}}**
Legal notices: **{{NOTICES_EMAIL}}**
Data protection: **dpo@eurobase.app**

---

## Annex A — Model Withdrawal Form **[CONSUMER]**

(Complete and return only if you wish to withdraw from the contract.)

To: **{{LEGAL_ENTITY}}**, **{{REGISTERED_ADDRESS}}**, **{{WITHDRAWAL_EMAIL}}**

I/We hereby give notice that I/we withdraw from my/our contract for the supply of the Eurobase service.

- Ordered on: ____________________
- Account email: ____________________
- Name of consumer(s): ____________________
- Address of consumer(s): ____________________
- Signature (only if on paper): ____________________
- Date: ____________________
