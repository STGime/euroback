package billing

import (
	"errors"
	"strings"
	"testing"
)

// TestProfileInput_Normalise pins the trim + uppercase behaviour
// on every field. A silent normalisation regression would cause
// invoices to render with mixed-case country codes / whitespace
// in the buyer name — visible to accountants.
func TestProfileInput_Normalise(t *testing.T) {
	in := ProfileInput{
		EntityType:    "  Business  ",
		LegalName:     "  Example Kaubandus OÜ  ",
		StreetAddress: " Ahtri 12 ",
		PostalCode:    " 15551 ",
		City:          " Tallinn ",
		Country:       "ee",
		RegistryCode:  " 12345678 ",
		VATNumber:     "ee123456789",
	}
	in.Normalise()

	cases := []struct {
		field string
		got   string
		want  string
	}{
		{"EntityType", in.EntityType, "business"},
		{"LegalName", in.LegalName, "Example Kaubandus OÜ"},
		{"StreetAddress", in.StreetAddress, "Ahtri 12"},
		{"PostalCode", in.PostalCode, "15551"},
		{"City", in.City, "Tallinn"},
		{"Country", in.Country, "EE"},
		{"RegistryCode", in.RegistryCode, "12345678"},
		{"VATNumber", in.VATNumber, "EE123456789"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.field, tc.got, tc.want)
		}
	}
}

// TestProfileInput_Validate walks every reject-and-accept branch.
// Each rejecting case pins the FIELD name so a future error-shape
// change doesn't silently move the highlight to a different input.
func TestProfileInput_Validate(t *testing.T) {
	base := func() ProfileInput {
		return ProfileInput{
			EntityType:    "business",
			LegalName:     "Example Kaubandus OÜ",
			StreetAddress: "Ahtri 12",
			PostalCode:    "15551",
			City:          "Tallinn",
			Country:       "EE",
			RegistryCode:  "12345678",
		}
	}

	t.Run("accepts a valid EE business", func(t *testing.T) {
		in := base()
		if err := in.Validate(); err != nil {
			t.Fatalf("valid input rejected: %v", err)
		}
	})

	t.Run("accepts a valid individual with empty registry_code", func(t *testing.T) {
		in := base()
		in.EntityType = "individual"
		in.RegistryCode = ""
		if err := in.Validate(); err != nil {
			t.Fatalf("valid individual rejected: %v", err)
		}
	})

	t.Run("accepts a non-EE business without registry_code", func(t *testing.T) {
		in := base()
		in.Country = "DE"
		in.RegistryCode = ""
		if err := in.Validate(); err != nil {
			t.Fatalf("valid DE business rejected: %v", err)
		}
	})

	t.Run("accepts optional VAT number when well-formed", func(t *testing.T) {
		in := base()
		in.VATNumber = "EE123456789"
		if err := in.Validate(); err != nil {
			t.Fatalf("valid VAT number rejected: %v", err)
		}
	})

	rejectCases := []struct {
		name      string
		mutate    func(*ProfileInput)
		wantField string
	}{
		{"invalid entity_type", func(p *ProfileInput) { p.EntityType = "corporation" }, "entity_type"},
		{"empty entity_type", func(p *ProfileInput) { p.EntityType = "" }, "entity_type"},
		{"legal_name too short", func(p *ProfileInput) { p.LegalName = "A" }, "legal_name"},
		{"legal_name too long", func(p *ProfileInput) { p.LegalName = strings.Repeat("x", 201) }, "legal_name"},
		{"street too short", func(p *ProfileInput) { p.StreetAddress = "" }, "street_address"},
		{"postal empty", func(p *ProfileInput) { p.PostalCode = "" }, "postal_code"},
		{"city empty", func(p *ProfileInput) { p.City = "" }, "city"},
		{"country lower-case", func(p *ProfileInput) { p.Country = "ee" }, "country"},
		{"country three letters", func(p *ProfileInput) { p.Country = "EST" }, "country"},
		{"country empty", func(p *ProfileInput) { p.Country = "" }, "country"},
		{"registry too short", func(p *ProfileInput) { p.RegistryCode = "1" }, "registry_code"},
		{"vat wrong shape", func(p *ProfileInput) { p.VATNumber = "12EE3456" }, "vat_number"},
		{"vat too short", func(p *ProfileInput) { p.VATNumber = "EE1" }, "vat_number"},
		{"EE business without registry", func(p *ProfileInput) {
			p.Country = "EE"
			p.EntityType = "business"
			p.RegistryCode = ""
		}, "registry_code"},
	}
	for _, tc := range rejectCases {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			tc.mutate(&in)
			err := in.Validate()
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			var vErr *ProfileValidationError
			if !errors.As(err, &vErr) {
				t.Fatalf("expected *ProfileValidationError, got %T: %v", err, err)
			}
			if vErr.Field != tc.wantField {
				t.Errorf("field = %q, want %q (message: %s)", vErr.Field, tc.wantField, vErr.Message)
			}
		})
	}
}
