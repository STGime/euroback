package workers

import (
	"context"
	"errors"
	"testing"

	"github.com/eurobase/euroback/internal/compliance"
	"github.com/eurobase/euroback/internal/dbprovider"
	"github.com/jackc/pgx/v5/pgxpool"
)

// lazyPool returns a pool that never connects unless used — enough to
// tell which pool resolveExportSource picked.
func lazyPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	p, err := pgxpool.New(context.Background(), "postgres://u:p@127.0.0.1:1/db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func TestResolveExportSource(t *testing.T) {
	ctx := context.Background()
	shared, dev, dedicated := lazyPool(t), lazyPool(t), lazyPool(t)
	data := compliance.ExportSource{Pool: dev}

	t.Run("Team-tier project reads its dedicated database", func(t *testing.T) {
		src, closeSrc, err := resolveExportSource(ctx, data, shared, func(context.Context, string) (*pgxpool.Pool, error) {
			return dedicated, nil
		}, "p1")
		if err != nil || src.Pool != dedicated || src.Role != "" {
			t.Fatalf("src = %+v, err = %v, want the dedicated pool, no role", src, err)
		}
		closeSrc() // closes the dedicated pool
	})
	t.Run("no dedicated database: shared cluster via the developer pool", func(t *testing.T) {
		src, closeSrc, err := resolveExportSource(ctx, data, shared, func(context.Context, string) (*pgxpool.Pool, error) {
			return nil, nil
		}, "p1")
		defer closeSrc()
		if err != nil || src.Pool != dev {
			t.Fatalf("src = %+v, err = %v, want the developer pool", src, err)
		}
	})
	t.Run("no developer pool (dev): the worker pool", func(t *testing.T) {
		src, closeSrc, err := resolveExportSource(ctx, compliance.ExportSource{}, shared, nil, "p1")
		defer closeSrc()
		if err != nil || src.Pool != shared {
			t.Fatalf("src = %+v, err = %v, want the worker pool", src, err)
		}
	})
	t.Run("dedicated database unavailable: error, never the shared cluster", func(t *testing.T) {
		src, closeSrc, err := resolveExportSource(ctx, data, shared, func(context.Context, string) (*pgxpool.Pool, error) {
			return nil, dbprovider.ErrDedicatedNotReady
		}, "p1")
		defer closeSrc()
		if !errors.Is(err, dbprovider.ErrDedicatedNotReady) || src.Pool != nil {
			t.Fatalf("src = %+v, err = %v, want ErrDedicatedNotReady and no source", src, err)
		}
	})
}
