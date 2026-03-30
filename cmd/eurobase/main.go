package main

import (
	"fmt"
	"os"

	"github.com/eurobase/euroback/internal/cli"
	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "eurobase",
		Short: "Eurobase CLI — EU-sovereign Backend-as-a-Service",
		Long:  "Manage your Eurobase projects, database, storage, and more from the terminal.",
	}

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
	root.AddCommand(cli.StorageCmd())
	root.AddCommand(cli.InitCmd())
	root.AddCommand(cli.TestCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
