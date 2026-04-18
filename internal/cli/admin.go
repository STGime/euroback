package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

// AdminCmd returns the "admin" parent command for platform-wide
// operations. Subcommands require a local DATABASE_URL (like the
// migrations command) rather than going through the gateway — they
// bootstrap the very permissions that the gateway would otherwise
// require. Only operators with direct DB access should be able to run
// these.
func AdminCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Platform administration (superadmin bootstrap)",
		Long: `Bootstrap commands for platform superadmins. Each subcommand requires
a local DATABASE_URL pointing at the platform database — the gateway API
is intentionally NOT used, because these commands exist to grant the very
flag the gateway's admin endpoints check for.`,
	}
	cmd.AddCommand(adminGrantCmd())
	cmd.AddCommand(adminRevokeCmd())
	cmd.AddCommand(adminListCmd())
	return cmd
}

func withPoolFromEnv(fn func(ctx context.Context, pool *pgxpool.Pool) error) error {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return fmt.Errorf("DATABASE_URL is not set in the environment. admin commands run directly against the platform DB and do not go through the gateway")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()
	return fn(ctx, pool)
}

func adminGrantCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "grant <email>",
		Short: "Mark a platform user as superadmin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			email := strings.ToLower(strings.TrimSpace(args[0]))
			return withPoolFromEnv(func(ctx context.Context, pool *pgxpool.Pool) error {
				tag, err := pool.Exec(ctx,
					`UPDATE public.platform_users SET is_superadmin = true WHERE email = $1`, email)
				if err != nil {
					return fmt.Errorf("update failed: %w", err)
				}
				if tag.RowsAffected() == 0 {
					return fmt.Errorf("no platform user with email %q", email)
				}
				PrintSuccess(fmt.Sprintf("Granted superadmin to %s", email))
				fmt.Println("The user must sign out and sign back in for their token to pick up the new flag.")
				return nil
			})
		},
	}
}

func adminRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <email>",
		Short: "Remove superadmin from a platform user",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			email := strings.ToLower(strings.TrimSpace(args[0]))
			return withPoolFromEnv(func(ctx context.Context, pool *pgxpool.Pool) error {
				tag, err := pool.Exec(ctx,
					`UPDATE public.platform_users SET is_superadmin = false WHERE email = $1`, email)
				if err != nil {
					return fmt.Errorf("update failed: %w", err)
				}
				if tag.RowsAffected() == 0 {
					return fmt.Errorf("no platform user with email %q", email)
				}
				PrintSuccess(fmt.Sprintf("Revoked superadmin from %s", email))
				return nil
			})
		},
	}
}

func adminListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List platform superadmins",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withPoolFromEnv(func(ctx context.Context, pool *pgxpool.Pool) error {
				rows, err := pool.Query(ctx,
					`SELECT email, created_at FROM public.platform_users
					 WHERE is_superadmin = true ORDER BY email`)
				if err != nil {
					return err
				}
				defer rows.Close()

				headers := []string{"Email", "Created"}
				var tableRows [][]string
				for rows.Next() {
					var email string
					var createdAt time.Time
					if err := rows.Scan(&email, &createdAt); err != nil {
						return err
					}
					tableRows = append(tableRows, []string{email, createdAt.Format("2006-01-02")})
				}
				if len(tableRows) == 0 {
					PrintWarning("No superadmins yet. Run: eurobase admin grant <email>")
					return nil
				}
				PrintTable(headers, tableRows)
				return nil
			})
		},
	}
}
