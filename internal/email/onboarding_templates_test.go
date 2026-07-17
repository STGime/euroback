package email

import (
	"strings"
	"testing"
)

// TestRenderOnboardingStep_AllSteps renders every step with a full
// data set and asserts the output contains the interpolated fields.
// Catches template typos (misspelled field, unbalanced tag) at
// package-test time rather than at 3 AM when the worker crashes.
func TestRenderOnboardingStep_AllSteps(t *testing.T) {
	data := OnboardingData{
		UserEmail:      "alice@example.com",
		DisplayName:    "Alice",
		ProjectName:    "my-project",
		UnsubscribeURL: "https://api.eurobase.app/platform/mailing/unsubscribe?token=xyz",
		DocsURL:        "https://console.eurobase.app/docs",
		ConsoleURL:     "https://console.eurobase.app",
	}
	for step := 0; step < NumOnboardingSteps; step++ {
		subject, body, err := RenderOnboardingStep(step, data)
		if err != nil {
			t.Errorf("step %d render: %v", step, err)
			continue
		}
		if subject == "" {
			t.Errorf("step %d: empty subject", step)
		}
		if !strings.Contains(body, data.UserEmail) {
			t.Errorf("step %d: body missing UserEmail", step)
		}
		if !strings.Contains(body, data.UnsubscribeURL) {
			t.Errorf("step %d: body missing UnsubscribeURL", step)
		}
	}
}

// TestRenderOnboardingStep_MinimalData ensures every template
// renders cleanly when the optional fields (DisplayName, ProjectName)
// are empty — this is a real path (users who signed up but haven't
// created a project yet, e.g. day-0 fire before their first project).
func TestRenderOnboardingStep_MinimalData(t *testing.T) {
	data := OnboardingData{
		UserEmail:      "bob@example.com",
		UnsubscribeURL: "https://api.eurobase.app/platform/mailing/unsubscribe?token=abc",
		DocsURL:        "https://console.eurobase.app/docs",
		ConsoleURL:     "https://console.eurobase.app",
	}
	for step := 0; step < NumOnboardingSteps; step++ {
		if _, _, err := RenderOnboardingStep(step, data); err != nil {
			t.Errorf("step %d minimal-data render: %v", step, err)
		}
	}
}

func TestRenderOnboardingStep_OutOfRange(t *testing.T) {
	_, _, err := RenderOnboardingStep(-1, OnboardingData{})
	if err == nil {
		t.Error("expected error for step=-1")
	}
	_, _, err = RenderOnboardingStep(NumOnboardingSteps, OnboardingData{})
	if err == nil {
		t.Errorf("expected error for step=%d", NumOnboardingSteps)
	}
}
