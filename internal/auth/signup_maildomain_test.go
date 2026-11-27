package auth

import (
	"context"
	"errors"
	"net"
	"testing"
)

type fakeMailResolver struct {
	mx      []*net.MX
	mxErr   error
	hosts   []string
	hostErr error
	asked   *[]string // names looked up, when set
}

func (f fakeMailResolver) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	if f.asked != nil {
		*f.asked = append(*f.asked, name)
	}
	return f.mx, f.mxErr
}
func (f fakeMailResolver) LookupHost(_ context.Context, name string) ([]string, error) {
	if f.asked != nil {
		*f.asked = append(*f.asked, name)
	}
	return f.hosts, f.hostErr
}

var (
	errNotFound = &net.DNSError{Err: "no such host", Name: "x", IsNotFound: true}
	errTimeout  = &net.DNSError{Err: "i/o timeout", Name: "x", IsTimeout: true}
	errServFail = &net.DNSError{Err: "server misbehaving", Name: "x", IsTemporary: true}
)

func TestCheckMailDomain(t *testing.T) {
	cases := []struct {
		name string
		r    fakeMailResolver
		want mailDomainVerdict
	}{
		{"has MX", fakeMailResolver{mx: []*net.MX{{Host: "mx.example.org.", Pref: 10}}}, mailDomainOK},
		{"null MX (RFC 7505)", fakeMailResolver{mx: []*net.MX{{Host: ".", Pref: 0}}}, mailDomainNone},
		{"no MX, has A", fakeMailResolver{mxErr: errNotFound, hosts: []string{"192.0.2.1"}}, mailDomainOK},
		{"empty MX answer, has A", fakeMailResolver{hosts: []string{"192.0.2.1"}}, mailDomainOK},
		{"domain doesn't exist", fakeMailResolver{mxErr: errNotFound, hostErr: errNotFound}, mailDomainNone},
		{"no MX, no A", fakeMailResolver{mxErr: errNotFound}, mailDomainNone},
		{"MX lookup times out", fakeMailResolver{mxErr: errTimeout}, mailDomainUnknown},
		{"MX server error", fakeMailResolver{mxErr: errServFail}, mailDomainUnknown},
		{"no MX, A lookup times out", fakeMailResolver{mxErr: errNotFound, hostErr: errTimeout}, mailDomainUnknown},
		{"other error", fakeMailResolver{mxErr: errors.New("boom")}, mailDomainUnknown},
	}
	for _, tc := range cases {
		got, _ := checkMailDomain(context.Background(), tc.r, "example.org")
		if got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

// Names are looked up absolute (no search list) and in punycode.
func TestCheckMailDomain_AbsolutePunycodeName(t *testing.T) {
	var asked []string
	r := fakeMailResolver{mxErr: errNotFound, hosts: []string{"192.0.2.1"}, asked: &asked}
	if v, _ := checkMailDomain(context.Background(), r, "münchen.de"); v != mailDomainOK {
		t.Fatalf("münchen.de: verdict %d, want ok", v)
	}
	want := []string{"xn--mnchen-3ya.de.", "xn--mnchen-3ya.de."}
	if len(asked) != 2 || asked[0] != want[0] || asked[1] != want[1] {
		t.Fatalf("looked up %v, want %v", asked, want)
	}
	asked = nil
	_, _ = checkMailDomain(context.Background(), r, "gmail.com")
	if len(asked) == 0 || asked[0] != "gmail.com." {
		t.Fatalf("looked up %v, want absolute gmail.com.", asked)
	}
	// A name IDNA can't convert is unknown (allowed), not refused.
	if v, _ := checkMailDomain(context.Background(), r, "a\u00a0b.example"); v != mailDomainUnknown {
		t.Fatalf("unconvertible name: verdict %d, want unknown", v)
	}
}

func TestRefuseUndeliverableDomain(t *testing.T) {
	ctx := context.Background()
	s := &PlatformAuthService{}
	// Off by default: no lookup, no refusal.
	if err := s.refuseUndeliverableDomain(ctx, "a@nowhere.invalid"); err != nil {
		t.Fatalf("check off: %v", err)
	}
	s.EnableMailDomainCheck(fakeMailResolver{mxErr: errNotFound, hostErr: errNotFound})
	if err := s.refuseUndeliverableDomain(ctx, "a@made-up.example"); !errors.Is(err, ErrNoMailDomain) {
		t.Fatalf("no mail domain: got %v, want ErrNoMailDomain", err)
	}
	s.EnableMailDomainCheck(fakeMailResolver{mxErr: errTimeout})
	if err := s.refuseUndeliverableDomain(ctx, "a@slow.example"); err != nil {
		t.Fatalf("lookup failure must let the signup through, got %v", err)
	}
	if !isUserError(ErrNoMailDomain) {
		t.Fatal("ErrNoMailDomain must be a user error (400)")
	}
}
