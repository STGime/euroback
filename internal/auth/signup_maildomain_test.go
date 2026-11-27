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
}

func (f fakeMailResolver) LookupMX(context.Context, string) ([]*net.MX, error) { return f.mx, f.mxErr }
func (f fakeMailResolver) LookupHost(context.Context, string) ([]string, error) {
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
