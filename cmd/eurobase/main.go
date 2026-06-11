package main

import (
	"fmt"
	"os"

	"github.com/eurobase/euroback/internal/cli"
	"github.com/spf13/cobra"
)

// Set at build time by the CLI release workflow:
//
//	go build -ldflags "-X main.version=1.2.3 -X main.commit=abc1234"
//
// "dev" identifies a from-source build — relevant when debugging user
// reports, since a stale local binary is indistinguishable from a release
// binary without this.
var (
	version = "dev"
	commit  = "none"
)

func main() {
	root := &cobra.Command{
		Use:     "eurobase",
		Short:   "Eurobase CLI — EU-sovereign Backend-as-a-Service",
		Long:    "Manage your Eurobase projects, database, storage, and more from the terminal.",
		Version: fmt.Sprintf("%s (commit %s)", version, commit),
	}

	// `eurobase version` for discoverability, alongside cobra's --version.
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the CLI version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("eurobase %s (commit %s)\n", version, commit)
		},
	})

	// Global flags
	root.PersistentFlags().String("api-url", "", "Override API URL")
	root.PersistentFlags().Bool("json", false, "Output as JSON")

	// Add all commands
	root.AddCommand(cli.LoginCmd())
	root.AddCommand(cli.LogoutCmd())
	root.AddCommand(cli.ProjectsCmd())
	root.AddCommand(cli.SwitchCmd())
	root.AddCommand(cli.StatusCmd())
	root.AddCommand(cli.DbCmd())
	root.AddCommand(cli.MigrationsCmd())
	root.AddCommand(cli.KeysCmd())
	root.AddCommand(cli.LogsCmd())
	root.AddCommand(cli.VaultCmd())
	root.AddCommand(cli.CronCmd())
	root.AddCommand(cli.FunctionsCmd())
	root.AddCommand(cli.EdgeFunctionsCmd())
	root.AddCommand(cli.StorageCmd())
	root.AddCommand(cli.InitCmd())
	root.AddCommand(cli.TestCmd())
	root.AddCommand(cli.ComplianceCmd())
	root.AddCommand(cli.AdminCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
