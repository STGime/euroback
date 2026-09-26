package storage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCreateBucket_ACLNotImplemented pins CreateBucket's handling of the
// follow-up PutBucketAcl(private): a server without bucket ACLs (Garage,
// local dev) answers 501 NotImplemented, which is accepted; any other
// refusal still fails the create.
func TestCreateBucket_ACLNotImplemented(t *testing.T) {
	for _, tc := range []struct {
		name       string
		aclStatus  int
		aclCode    string
		wantErrNil bool
	}{
		{"not implemented is accepted", http.StatusNotImplemented, "NotImplemented", true},
		{"access denied still fails", http.StatusForbidden, "AccessDenied", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPut || r.URL.Path != "/b" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if _, isACL := r.URL.Query()["acl"]; isACL {
					w.Header().Set("Content-Type", "application/xml")
					w.WriteHeader(tc.aclStatus)
					_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>` + tc.aclCode + `</Code><Message>x</Message></Error>`))
					return
				}
				w.WriteHeader(http.StatusOK) // CreateBucket
			}))
			defer srv.Close()

			c, err := NewS3Client(srv.URL, "fr-par", "key", "secret")
			if err != nil {
				t.Fatal(err)
			}
			err = c.CreateBucket(context.Background(), "b")
			if tc.wantErrNil && err != nil {
				t.Fatalf("want nil, got %v", err)
			}
			if !tc.wantErrNil && err == nil {
				t.Fatal("want an error, got nil")
			}
		})
	}
}
