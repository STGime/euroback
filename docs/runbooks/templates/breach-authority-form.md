<!--
Supervisory authority paste-in — rendered programmatically by
internal/breach/notify.go (RenderAuthorityForm). The DPO files this
through the SA's portal within 72 hours of awareness.

Each EU SA accepts its own form. This document is the structured
content; the DPO copies the relevant sections into the SA portal's
text fields. Section numbering matches Art. 33(3).
-->

# Personal-Data Breach Notification — GDPR Art. 33

**Filer:** Eurobase SAS (controller for platform data; processor for tenant data)
**Lead supervisory authority:** {{LEAD_SA}}
**Incident reference:** `{{INCIDENT_ID}}`
**DPO:** dpo@eurobase.app

## 1. Nature of the breach

{{INCIDENT_NATURE}}

- **Title:** {{INCIDENT_TITLE}}
- **Window of occurrence:** {{INCIDENT_WINDOW}} (UTC)
- **Awareness:** {{INCIDENT_AWARENESS_AT}} (UTC)
- **Time elapsed to this notification:** {{ELAPSED_HOURS}} hours

## 2. Data and subjects

- **Categories of personal data:** {{DATA_CATEGORIES}}
- **Categories of data subjects:** {{SUBJECT_CATEGORIES}}
- **Approximate number of records:** {{RECORDS_AFFECTED}}
- **Approximate number of subjects:** {{SUBJECTS_AFFECTED}}

## 3. Likely consequences of the breach

{{LIKELY_CONSEQUENCES}}

## 4. Measures taken or proposed

{{MEASURES_TAKEN}}

## 5. Contact point

DPO: dpo@eurobase.app
Eurobase incident reference: `{{INCIDENT_ID}}`

---

**Filing notes**

- If notification is more than 72 hours after awareness, attach the
  written reason for delay (Art. 33(1) sentence 2).
- File even partial information if some fields are still "(pending)".
  Follow up via the SA's "supplementary information" path as
  investigation completes.
- After filing, update the breach register by calling
  `POST /compliance/breaches/{incidentId}/authority-form` with
  `{"filed": true, "lead_sa": "<code>"}` so the register reflects the
  notification timestamp and the SLA dashboard turns green.
