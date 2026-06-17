<!--
Customer notification template — referenced from
docs/runbooks/breach-notification.md and rendered programmatically by
internal/breach/notify.go (RenderCustomerEmail).

DPA §10 commits us to sending this within 24 hours of becoming aware of
a personal-data breach affecting Customer Data. Send via the
/platform/projects/{id}/compliance/breaches/{incidentId}/notify-customers
endpoint so the dispatch is recorded on the append-only register.

Hand-edit only the placeholders below. Keep the structure unchanged —
the Art. 33(3) bullets are what makes the notice complete.
-->

# Customer notification email — Eurobase breach

**Subject:** [Eurobase] Personal-data breach notice ({{INCIDENT_SHORT_ID}})

**To:** {{CUSTOMER_BILLING_EMAIL}}, {{CUSTOMER_DPO_EMAIL}}
(send via BCC; recipients do not see each other's address)

---

Hello,

Under our Data Processing Agreement (§10) and GDPR Art. 33, we are
writing to notify you of a personal-data breach that affects your data
processed on Eurobase. This notice is sent within 24 hours of our
becoming aware of the incident.

## What happened

{{INCIDENT_NATURE}}

- **Incident reference:** `{{INCIDENT_ID}}`
- **Window:** {{INCIDENT_WINDOW}} (UTC)
- **We became aware:** {{INCIDENT_AWARENESS_AT}} (UTC)

## What data is affected

- **Categories of data:** {{DATA_CATEGORIES}}
- **Categories of data subjects:** {{SUBJECT_CATEGORIES}}
- **Approximate records affected:** {{RECORDS_AFFECTED}}
- **Approximate subjects affected:** {{SUBJECTS_AFFECTED}}

## Likely consequences

{{LIKELY_CONSEQUENCES}}

## What we have done

{{MEASURES_TAKEN}}

## What we need from you

You are the controller for end-user data on Eurobase. You may have
your own notification obligations under GDPR Art. 34 toward your end
users. We will provide any additional information you need.

## Point of contact

DPO: dpo@eurobase.app

— The Eurobase team

---

**Internal post-send checklist**

- [ ] Confirm send recorded on `breach_register` (status =
      `notified_customers`).
- [ ] Confirm `audit_log` entry `breach.notified_customers` is
      present and the hash chain still verifies.
- [ ] Slack #incident-{{INCIDENT_SHORT_ID}} with the BulkResult.
