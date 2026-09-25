package pgbouncerconf

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/eurobase/euroback/internal/k8sapi"
	"github.com/eurobase/euroback/internal/tenantlogin"
	"github.com/jackc/pgx/v5"
)

// Published tenant userlist (#653). The worker — which holds
// FUNC_PASSWORD_SECRET anyway — derives each tenant's SCRAM verifier and
// publishes the userlist lines plus the upstream address; the runner's
// pooler only reads them. So the pooler pod, which tenant code can reach,
// holds no master secret and no database password: a compromise there
// yields verifiers only (no login without the password; offline brute
// force of a 64-hex HMAC is not practical).
const (
	KeyUserlist = "userlist" // tenant `"<schema>_func" "<verifier>"` lines
	KeyUpstream = "upstream" // "host:port/database" (no credentials)
)

// Store is where the published userlist lives: a Kubernetes Secret in
// production, a directory in tests.
type Store interface {
	Get(ctx context.Context) (map[string][]byte, error)
	Put(ctx context.Context, data map[string][]byte) error
}

// SecretStore is a Kubernetes Secret (read: pooler SA; patch: worker SA).
type SecretStore struct {
	Client *k8sapi.Client
	Name   string
}

func (s SecretStore) Get(ctx context.Context) (map[string][]byte, error) {
	return s.Client.GetSecretData(ctx, s.Name)
}

func (s SecretStore) Put(ctx context.Context, data map[string][]byte) error {
	return s.Client.PatchSecretData(ctx, s.Name, data)
}

// DirStore keeps one file per key in a directory (tests).
type DirStore string

func (d DirStore) Get(context.Context) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, k := range []string{KeyUserlist, KeyUpstream} {
		b, err := os.ReadFile(filepath.Join(string(d), k))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[k] = b
	}
	return out, nil
}

func (d DirStore) Put(_ context.Context, data map[string][]byte) error {
	for k, v := range data {
		tmp := filepath.Join(string(d), "."+k+".tmp")
		if err := os.WriteFile(tmp, v, 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, filepath.Join(string(d), k)); err != nil {
			return err
		}
	}
	return nil
}

// RenderTenantUserlist returns the tenant lines of the userlist.
func RenderTenantUserlist(secret []byte, schemas []string) (string, error) {
	var b strings.Builder
	for _, s := range schemas {
		v, err := tenantlogin.ScramVerifier(secret, s)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%s %s\n", quote(tenantlogin.FuncRole(s)), quote(v))
	}
	return b.String(), nil
}

// UpstreamOf returns "host:port/database" for a postgres:// URL.
func UpstreamOf(databaseURL string) (string, error) {
	var s Settings
	if err := s.UpstreamFromURL(databaseURL); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s:%d/%s", s.Host, s.Port, s.Database), nil
}

// ParseUpstream fills Host/Port/Database from "host:port/database".
func (s *Settings) ParseUpstream(upstream string) error {
	return s.UpstreamFromURL("postgres://" + strings.TrimSpace(upstream))
}

// Publisher keeps the published userlist current (worker side).
type Publisher struct {
	Store    Store
	Secret   []byte
	Upstream string
	last     string
}

// Publish lists tenant schemas and writes the userlist when it changed.
// Returns whether it wrote.
func (p *Publisher) Publish(ctx context.Context, conn *pgx.Conn) (bool, error) {
	schemas, err := TenantSchemas(ctx, conn)
	if err != nil {
		return false, err
	}
	body, err := RenderTenantUserlist(p.Secret, schemas)
	if err != nil {
		return false, err
	}
	if body == p.last {
		return false, nil
	}
	if err := p.Store.Put(ctx, map[string][]byte{KeyUserlist: []byte(body), KeyUpstream: []byte(p.Upstream)}); err != nil {
		return false, err
	}
	p.last = body
	return true, nil
}
