package compliance

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// DPAReport is the full GDPR Data Processing Agreement compliance report
// for a specific Eurobase project.
type DPAReport struct {
	GeneratedAt          time.Time            `json:"generated_at"`
	Version              string               `json:"version"`
	EurobaseEntity       EntityInfo           `json:"eurobase_entity"`
	Customer             CustomerInfo         `json:"customer"`
	SubProcessors        []SubProcessor       `json:"sub_processors"`
	DataFlow             DataFlowInfo         `json:"data_flow"`
	ProcessingActivities []ProcessingActivity `json:"processing_activities"`
	Summary              ReportSummary        `json:"summary"`
}

// EntityInfo describes the Eurobase legal entity.
type EntityInfo struct {
	Name     string `json:"name"`
	Country  string `json:"country"`
	DPOEmail string `json:"dpo_email"`
}

// CustomerInfo describes the customer project context.
type CustomerInfo struct {
	ProjectName string `json:"project_name"`
	ProjectSlug string `json:"project_slug"`
	Plan        string `json:"plan"`
}

// DataFlowInfo describes how data flows through the Eurobase infrastructure.
type DataFlowInfo struct {
	StorageLocation      string `json:"storage_location"`
	EncryptionAtRest     bool   `json:"encryption_at_rest"`
	EncryptionInTransit  bool   `json:"encryption_in_transit"`
	CrossBorderTransfers bool   `json:"cross_border_transfers"`
	CrossBorderDetails   string `json:"cross_border_details,omitempty"`
}

// ProcessingActivity describes a single GDPR Article 30 processing activity.
type ProcessingActivity struct {
	Activity       string   `json:"activity"`
	LegalBasis     string   `json:"legal_basis"`
	DataCategories []string `json:"data_categories"`
	Retention      string   `json:"retention"`
}

// ReportSummary provides a high-level overview of the compliance posture.
type ReportSummary struct {
	TotalSubProcessors int    `json:"total_sub_processors"`
	EUOnly             bool   `json:"eu_only"`
	CloudActExposure   bool   `json:"cloud_act_exposure"`
	CloudActDetails    string `json:"cloud_act_details,omitempty"`
}

// GenerateReport builds a full DPA compliance report for the given project.
func (s *ComplianceService) GenerateReport(ctx context.Context, projectID string) (*DPAReport, error) {
	// 1. Get project config.
	pc, err := s.getProjectConfig(ctx, projectID)
	if err != nil {
		return nil, err
	}

	// 2. Resolve active features.
	features := s.resolveActiveFeatures(ctx, pc)

	// 3. Get active sub-processors.
	processors, err := s.GetActiveSubProcessors(ctx, projectID)
	if err != nil {
		return nil, err
	}

	// 4. Build data flow info.
	hasCrossBorder := false
	var crossBorderProviders []string
	for _, sp := range processors {
		if sp.Jurisdiction != "EU" {
			hasCrossBorder = true
			crossBorderProviders = append(crossBorderProviders, fmt.Sprintf("%s (%s — %s)", sp.Name, sp.Country, sp.TransferMechanism))
		}
	}

	dataFlow := DataFlowInfo{
		StorageLocation:      "France (Scaleway DC-PAR1 / DC-PAR2)",
		EncryptionAtRest:     true,
		EncryptionInTransit:  true,
		CrossBorderTransfers: hasCrossBorder,
	}
	if hasCrossBorder {
		dataFlow.CrossBorderDetails = "Cross-border transfers occur for optional OAuth providers only: " + strings.Join(crossBorderProviders, "; ")
	}

	// 5. Build processing activities based on enabled features.
	featureSet := make(map[string]bool, len(features))
	for _, f := range features {
		featureSet[f] = true
	}
	activities := buildProcessingActivities(featureSet, pc.Plan)

	// 6. Calculate summary.
	euOnly := !hasCrossBorder
	cloudActExposure := false
	var cloudActProviders []string
	for _, sp := range processors {
		if sp.CloudActRisk {
			cloudActExposure = true
			cloudActProviders = append(cloudActProviders, sp.Name)
		}
	}

	summary := ReportSummary{
		TotalSubProcessors: len(processors),
		EUOnly:             euOnly,
		CloudActExposure:   cloudActExposure,
	}
	if cloudActExposure {
		summary.CloudActDetails = fmt.Sprintf(
			"The following sub-processors are subject to the US CLOUD Act: %s. "+
				"These are only active because the project has enabled OAuth sign-in with US providers. "+
				"Disabling these OAuth providers will remove all CLOUD Act exposure.",
			strings.Join(cloudActProviders, ", "),
		)
	}

	report := &DPAReport{
		GeneratedAt: time.Now().UTC(),
		Version:     "1.0",
		EurobaseEntity: EntityInfo{
			Name:     "Eurobase",
			Country:  "EU",
			DPOEmail: "dpo@eurobase.app",
		},
		Customer: CustomerInfo{
			ProjectName: pc.Name,
			ProjectSlug: pc.Slug,
			Plan:        pc.Plan,
		},
		SubProcessors:        processors,
		DataFlow:             dataFlow,
		ProcessingActivities: activities,
		Summary:              summary,
	}

	return report, nil
}

// buildProcessingActivities returns the standard set of GDPR Article 30
// processing activities, filtered to only those relevant for the project.
func buildProcessingActivities(features map[string]bool, plan string) []ProcessingActivity {
	var activities []ProcessingActivity

	// User authentication — always present (database is always on).
	activities = append(activities, ProcessingActivity{
		Activity:       "User authentication",
		LegalBasis:     "Art. 6(1)(b) — performance of contract",
		DataCategories: []string{"email", "password_hash", "session_tokens"},
		Retention:      "Account lifetime",
	})

	// Data storage — always present.
	activities = append(activities, ProcessingActivity{
		Activity:       "Data storage",
		LegalBasis:     "Art. 6(1)(b) — performance of contract",
		DataCategories: []string{"application_data"},
		Retention:      "Customer-defined retention",
	})

	// File storage.
	if features["storage"] {
		activities = append(activities, ProcessingActivity{
			Activity:       "File storage",
			LegalBasis:     "Art. 6(1)(b) — performance of contract",
			DataCategories: []string{"files", "metadata"},
			Retention:      "Until deletion",
		})
	}

	// Email delivery.
	if features["email"] {
		activities = append(activities, ProcessingActivity{
			Activity:       "Email delivery",
			LegalBasis:     "Art. 6(1)(b) — performance of contract",
			DataCategories: []string{"email_addresses", "email_content"},
			Retention:      "30 days",
		})
	}

	// Request logging — always present (compute is always on).
	var logRetention string
	switch plan {
	case "pro":
		logRetention = "7 days"
	case "business":
		logRetention = "30 days"
	default:
		logRetention = "1 day"
	}
	activities = append(activities, ProcessingActivity{
		Activity:       "Request logging",
		LegalBasis:     "Art. 6(1)(f) — legitimate interest",
		DataCategories: []string{"ip_addresses", "user_agent", "request_path"},
		Retention:      logRetention,
	})

	// Payment processing.
	if features["billing"] {
		activities = append(activities, ProcessingActivity{
			Activity:       "Payment processing",
			LegalBasis:     "Art. 6(1)(b) — performance of contract",
			DataCategories: []string{"payment_data"},
			Retention:      "Legal requirement (7 years)",
		})
	}

	return activities
}
