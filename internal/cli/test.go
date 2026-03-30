package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// TestCmd returns the "eurobase test" command.
func TestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test [file-or-directory]",
		Short: "Run pgTAP database tests",
		Long: `Run pgTAP tests against your project database.

Tests are SQL files that use pgTAP assertions to verify RLS policies,
triggers, functions, and data integrity.

Example test file (tests/rls_tasks.sql):

  BEGIN;
  SELECT plan(2);

  SET LOCAL app.end_user_id = 'alice-uuid';
  SELECT ok(
      EXISTS(SELECT 1 FROM tasks WHERE user_id = 'alice-uuid'),
      'Alice can see her own tasks'
  );
  SELECT ok(
      NOT EXISTS(SELECT 1 FROM tasks WHERE user_id = 'bob-uuid'),
      'Alice cannot see Bob tasks'
  );

  SELECT * FROM finish();
  ROLLBACK;

Usage:
  eurobase test                     # Run all .sql files in ./tests/
  eurobase test tests/rls.sql       # Run a specific test file
  eurobase test tests/              # Run all tests in a directory`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return fmt.Errorf("not logged in — run 'eurobase login' first")
			}
			if err := RequireProject(cfg); err != nil {
				return err
			}

			client, err := NewClientFromConfig()
			if err != nil {
				return err
			}

			// Determine test files.
			target := "./tests"
			if len(args) > 0 {
				target = args[0]
			}

			files, err := findTestFiles(target)
			if err != nil {
				return err
			}
			if len(files) == 0 {
				PrintWarning("No test files found. Create .sql files in ./tests/ directory.")
				fmt.Println("\nExample test (tests/rls_example.sql):")
				fmt.Println(`
  BEGIN;
  SELECT plan(2);

  -- Test as authenticated user
  SET LOCAL app.end_user_id = '<user-uuid>';

  SELECT ok(
      EXISTS(SELECT 1 FROM my_table WHERE user_id = '<user-uuid>'),
      'User can see own rows'
  );

  SELECT ok(
      NOT EXISTS(SELECT 1 FROM my_table WHERE user_id = '<other-uuid>'),
      'User cannot see other rows'
  );

  SELECT * FROM finish();
  ROLLBACK;`)
				return nil
			}

			fmt.Printf("Running %d test file(s)...\n\n", len(files))

			passed := 0
			failed := 0

			for _, file := range files {
				sql, err := os.ReadFile(file)
				if err != nil {
					PrintError(fmt.Sprintf("Cannot read %s: %v", file, err))
					failed++
					continue
				}

				// Execute the test SQL.
				resp, err := client.Post(
					fmt.Sprintf("/platform/projects/%s/data/sql", cfg.ActiveProject),
					map[string]interface{}{"sql": string(sql), "limit": 1000},
				)
				if err != nil {
					PrintError(fmt.Sprintf("%s — %v", filepath.Base(file), err))
					failed++
					continue
				}

				var result struct {
					Columns []string                 `json:"columns"`
					Rows    []map[string]interface{} `json:"rows"`
				}
				if err := json.Unmarshal(resp, &result); err != nil {
					PrintError(fmt.Sprintf("%s — failed to parse response", filepath.Base(file)))
					failed++
					continue
				}

				// Check for pgTAP output.
				hasFailure := false
				for _, row := range result.Rows {
					for _, v := range row {
						line := fmt.Sprintf("%v", v)
						if strings.Contains(line, "not ok") {
							hasFailure = true
						}
						// Print pgTAP output lines.
						if strings.HasPrefix(line, "ok ") || strings.HasPrefix(line, "not ok ") || strings.HasPrefix(line, "1..") || strings.HasPrefix(line, "#") {
							fmt.Printf("  %s\n", line)
						}
					}
				}

				if hasFailure {
					PrintError(filepath.Base(file))
					failed++
				} else {
					PrintSuccess(filepath.Base(file))
					passed++
				}
			}

			fmt.Printf("\n%d passed, %d failed\n", passed, failed)

			if failed > 0 {
				return fmt.Errorf("%d test(s) failed", failed)
			}
			return nil
		},
	}

	return cmd
}

func findTestFiles(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	if !info.IsDir() {
		return []string{path}, nil
	}

	var files []string
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, filepath.Join(path, e.Name()))
		}
	}
	return files, nil
}
