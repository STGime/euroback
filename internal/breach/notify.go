package breach

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"strings"
	"time"
)

// Mailer is the minimal interface the breach service needs to deliver
// notifications. internal/email.EmailService implements both methods.
type Mailer interface {
	SendBulkBCC(ctx context.Context, recipients []string, subject, htmlBody string) (BulkSendResult, error)
	SendRaw(ctx context.Context, to, subject, htmlBody string) error
}

// BulkSendResult mirrors internal/email.BulkResult so the breach service
// doesn't take an import cycle on internal/email. The handler adapts the
// concrete type.
type BulkSendResult struct {
	Sent   int
	Failed int
}

// CustomerEmailData is the variable surface available to the customer
// notification template. The runbook's "to the best of current knowledge"
// fields all live here.
type CustomerEmailData struct {
	IncidentID         string
	Title              string
	OccurredWindow     string
	AwarenessAt        string
	Nature             string
	DataCategories     string
	SubjectCategories  string
	RecordsAffected    string
	SubjectsAffected   string
	LikelyConsequences string
	MeasuresTaken      string
	DPOEmail           string
}

// AuthorityFormData populates the supervisory-authority free-form summary.
// The actual filing is done by the DPO in the SA's portal; we provide a
// structured paste-in that already covers every Art. 33(3) bullet.
type AuthorityFormData struct {
	IncidentID         string
	Title              string
	OccurredWindow     string
	AwarenessAt        string
	Nature             string
	DataCategories     string
	SubjectCategories  string
	RecordsAffected    string
	SubjectsAffected   string
	LikelyConsequences string
	MeasuresTaken      string
	LeadSA             string
	DPOEmail           string
}

const customerEmailTpl = `<!DOCTYPE html>
<html><body style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;color:#18181b;max-width:640px;margin:0 auto;padding:24px">
<h2 style="margin:0 0 16px">Notice of a personal-data breach affecting your Eurobase project</h2>
<p>Hello,</p>
<p>Under our Data Processing Agreement (§10) and GDPR Art. 33, we are
writing to notify you of a personal-data breach that affects your data
processed on Eurobase. This notice is sent within 24 hours of our
becoming aware of the incident.</p>

<h3>What happened</h3>
<p>{{.Nature}}</p>
<p><strong>Incident reference:</strong> {{.IncidentID}}<br>
<strong>Window:</strong> {{.OccurredWindow}}<br>
<strong>We became aware:</strong> {{.AwarenessAt}} UTC</p>

<h3>What data is affected</h3>
<ul>
<li><strong>Categories of data:</strong> {{.DataCategories}}</li>
<li><strong>Categories of data subjects:</strong> {{.SubjectCategories}}</li>
<li><strong>Approximate records affected:</strong> {{.RecordsAffected}}</li>
<li><strong>Approximate subjects affected:</strong> {{.SubjectsAffected}}</li>
</ul>

<h3>Likely consequences</h3>
<p>{{.LikelyConsequences}}</p>

<h3>What we have done</h3>
<p>{{.MeasuresTaken}}</p>

<h3>What we need from you</h3>
<p>You are the controller for end-user data on Eurobase. You may have
your own notification obligations under GDPR Art. 34 toward your end
users. We are available to support that process and will provide any
additional information you need.</p>

<p>Point of contact: <a href="mailto:{{.DPOEmail}}">{{.DPOEmail}}</a> (Eurobase DPO).</p>

<p>— The Eurobase team</p>
</body></html>`

const authorityFormTpl = `# Personal-Data Breach Notification under GDPR Art. 33

**Filer:** Eurobase (controller for platform data; processor for tenant data)
**Lead supervisory authority:** {{.LeadSA}}
**Incident reference:** {{.IncidentID}}
**DPO:** {{.DPOEmail}}

## 1. Nature of the breach
{{.Nature}}

**Title:** {{.Title}}
**Window:** {{.OccurredWindow}}
**Awareness:** {{.AwarenessAt}} UTC

## 2. Data and subjects
- **Categories of personal data:** {{.DataCategories}}
- **Categories of data subjects:** {{.SubjectCategories}}
- **Approximate number of records:** {{.RecordsAffected}}
- **Approximate number of subjects:** {{.SubjectsAffected}}

## 3. Likely consequences of the breach
{{.LikelyConsequences}}

## 4. Measures taken or proposed
{{.MeasuresTaken}}

## 5. Contact point
DPO: {{.DPOEmail}}
`

// RenderCustomerEmail produces the (subject, html) pair sent to controllers.
func RenderCustomerEmail(e *Entry, dpoEmail string) (string, string, error) {
	data := newCustomerData(e, dpoEmail)
	subject := fmt.Sprintf("[Eurobase] Personal-data breach notice (%s)", shortID(e.IncidentID))
	body, err := renderHTML(customerEmailTpl, data)
	if err != nil {
		return "", "", err
	}
	return subject, body, nil
}

// RenderAuthorityForm produces the Markdown paste-in for the supervisory
// authority. Returns plain text — the DPO files it through the SA's portal.
func RenderAuthorityForm(e *Entry, dpoEmail string) (string, error) {
	data := AuthorityFormData{
		IncidentID:         e.IncidentID,
		Title:              e.Title,
		OccurredWindow:     formatWindow(e.OccurredAt, e.OccurredUntil),
		AwarenessAt:        e.AwarenessAt.UTC().Format(time.RFC3339),
		Nature:             ifEmpty(e.Description, "(pending)"),
		DataCategories:     ifEmptySlice(e.DataCategories, "(pending)"),
		SubjectCategories:  ifEmptySlice(e.SubjectCategories, "(pending)"),
		RecordsAffected:    formatInt64Ptr(e.RecordsAffected, "(pending)"),
		SubjectsAffected:   formatInt64Ptr(e.SubjectsAffected, "(pending)"),
		LikelyConsequences: ifEmpty(e.LikelyConsequences, "(pending)"),
		MeasuresTaken:      ifEmpty(e.MeasuresTaken, "(pending)"),
		LeadSA:             derefStr(e.LeadSA, "(unset)"),
		DPOEmail:           dpoEmail,
	}
	t, err := template.New("auth").Parse(authorityFormTpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// SendCustomerNotice renders and BCCs the customer notice to the given
// recipients. Returns (sent, failed) like the underlying SendBulkBCC.
func SendCustomerNotice(ctx context.Context, mailer Mailer, e *Entry, recipients []string, dpoEmail string) (BulkSendResult, error) {
	if mailer == nil {
		return BulkSendResult{}, fmt.Errorf("mailer not configured")
	}
	if len(recipients) == 0 {
		return BulkSendResult{}, fmt.Errorf("no recipients")
	}
	subject, html, err := RenderCustomerEmail(e, dpoEmail)
	if err != nil {
		return BulkSendResult{}, err
	}
	return mailer.SendBulkBCC(ctx, recipients, subject, html)
}

// ── helpers ───────────────────────────────────────────────────────────────

func newCustomerData(e *Entry, dpoEmail string) CustomerEmailData {
	return CustomerEmailData{
		IncidentID:         e.IncidentID,
		Title:              e.Title,
		OccurredWindow:     formatWindow(e.OccurredAt, e.OccurredUntil),
		AwarenessAt:        e.AwarenessAt.UTC().Format(time.RFC3339),
		Nature:             ifEmpty(e.Description, "Details are still being established. We will follow up as our investigation completes."),
		DataCategories:     ifEmptySlice(e.DataCategories, "under investigation"),
		SubjectCategories:  ifEmptySlice(e.SubjectCategories, "under investigation"),
		RecordsAffected:    formatInt64Ptr(e.RecordsAffected, "under investigation"),
		SubjectsAffected:   formatInt64Ptr(e.SubjectsAffected, "under investigation"),
		LikelyConsequences: ifEmpty(e.LikelyConsequences, "under assessment"),
		MeasuresTaken:      ifEmpty(e.MeasuresTaken, "Stop-the-bleeding measures are in place; root cause analysis is ongoing."),
		DPOEmail:           dpoEmail,
	}
}

func renderHTML(tpl string, data interface{}) (string, error) {
	t, err := template.New("email").Parse(tpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func formatWindow(from, until *time.Time) string {
	if from == nil && until == nil {
		return "under investigation"
	}
	if from != nil && until == nil {
		return from.UTC().Format(time.RFC3339) + " — ongoing"
	}
	if from == nil && until != nil {
		return "unknown — " + until.UTC().Format(time.RFC3339)
	}
	return from.UTC().Format(time.RFC3339) + " — " + until.UTC().Format(time.RFC3339)
}

func ifEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func ifEmptySlice(s []string, fallback string) string {
	if len(s) == 0 {
		return fallback
	}
	return strings.Join(s, ", ")
}

func formatInt64Ptr(v *int64, fallback string) string {
	if v == nil {
		return fallback
	}
	return fmt.Sprintf("%d", *v)
}

func derefStr(p *string, fallback string) string {
	if p == nil || *p == "" {
		return fallback
	}
	return *p
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
