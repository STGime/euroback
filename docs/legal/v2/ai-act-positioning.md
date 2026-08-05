# AI Act positioning

**Where Eurobase sits under Regulation (EU) 2024/1689 (the AI Act).**

The AI Act's high-risk provisions apply from August 2026 for existing systems; from August 2027 for high-risk systems in Annex III. Eurobase's position:

## We are infrastructure, not an "AI system"

Eurobase provides Backend-as-a-Service infrastructure — database, storage, auth, realtime, edge functions. We do not embed any AI model or AI-based decision logic in the platform surface Customers use. We are neither an **AI system provider** (Art. 3(3)) nor a **general-purpose AI model provider** (Art. 3(63)).

If a Customer builds an AI application on top of Eurobase (e.g. a legal-tech tool that classifies documents, a health-tech tool that scores images), the **Customer** is the AI system provider / deployer. Their obligations under the AI Act (conformity assessment, risk management, transparency, human oversight) apply to the Customer's system, not to Eurobase.

We do not fine-tune, host, or serve any AI model on the Customer's behalf.

## Annex III alignment

Where our Customers build AI systems in Annex III categories — including **№8 (Justice and democratic processes)**, which covers many legal-tech use cases — they are the "deployer" or "provider" as defined in the Regulation. Eurobase's contract does not shift those roles.

We commit to:

- Not roll out any embedded AI feature on the platform without a prior AI Act assessment and 60-day advance notice to Legal Team Customers.
- Provide Customers on request with a written statement confirming Eurobase's non-AI-system status for their internal AI Act documentation.

## If we ever add AI features

Should Eurobase in the future introduce an AI-based feature (e.g. semantic search on stored objects, an AI-assisted SQL editor in the console), the feature will be:

- Opt-in per project (Customer must enable).
- Documented in the DPA and this positioning statement, updated in the same release.
- Assessed against the Annex III risk categories relevant to the use case.

No such features are on the roadmap as of this document's effective date.

## Contact

**dpo@eurobase.app**
