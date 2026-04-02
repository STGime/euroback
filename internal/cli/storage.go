package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

// StorageCmd returns the parent "storage" command.
func StorageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "storage",
		Short: "Manage file storage",
	}
	cmd.AddCommand(storageLsCmd())
	cmd.AddCommand(storageUploadCmd())
	cmd.AddCommand(storageDownloadCmd())
	cmd.AddCommand(storageDeleteCmd())
	cmd.AddCommand(storageUrlCmd())
	return cmd
}

func storageLsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List files in storage",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			if err := RequireProject(cfg); err != nil {
				return err
			}

			client, err := NewClientFromConfig()
			if err != nil {
				return err
			}

			prefix, _ := cmd.Flags().GetString("prefix")
			path := "/platform/projects/" + cfg.ActiveProject + "/storage"
			if prefix != "" {
				path += "?prefix=" + prefix
			}

			data, err := client.Get(path)
			if err != nil {
				return err
			}

			var resp struct {
				Objects []struct {
					Key      string `json:"key"`
					Size     int64  `json:"size"`
					Type     string `json:"content_type"`
					Modified string `json:"last_modified"`
				} `json:"objects"`
			}
			if err := json.Unmarshal(data, &resp); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}
			files := resp.Objects

			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				PrintJSON(files)
				return nil
			}

			headers := []string{"Key", "Size", "Type", "Modified"}
			var rows [][]string
			for _, f := range files {
				rows = append(rows, []string{
					f.Key,
					formatSize(f.Size),
					f.Type,
					f.Modified,
				})
			}
			PrintTable(headers, rows)
			return nil
		},
	}
	cmd.Flags().StringP("prefix", "p", "", "Filter by key prefix")
	return cmd
}

func storageUploadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "upload <local-path> <remote-key>",
		Short: "Upload a file to storage",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			if err := RequireProject(cfg); err != nil {
				return err
			}

			localPath := args[0]
			remoteKey := args[1]

			file, err := os.Open(localPath)
			if err != nil {
				return fmt.Errorf("opening file: %w", err)
			}
			defer file.Close()

			// Create multipart form
			pr, pw := io.Pipe()
			writer := multipart.NewWriter(pw)

			go func() {
				defer pw.Close()
				defer writer.Close()

				_ = writer.WriteField("key", remoteKey)

				part, err := writer.CreateFormFile("file", filepath.Base(localPath))
				if err != nil {
					pw.CloseWithError(err)
					return
				}
				if _, err := io.Copy(part, file); err != nil {
					pw.CloseWithError(err)
					return
				}
			}()

			cfgLoaded, _ := LoadConfig()
			url := cfgLoaded.APIURL + "/platform/projects/" + cfg.ActiveProject + "/storage/upload"
			req, err := http.NewRequest(http.MethodPost, url, pr)
			if err != nil {
				return fmt.Errorf("creating request: %w", err)
			}
			req.Header.Set("Authorization", "Bearer "+cfgLoaded.Token)
			req.Header.Set("Content-Type", writer.FormDataContentType())

			httpClient := &http.Client{Timeout: 5 * time.Minute}
			resp, err := httpClient.Do(req)
			if err != nil {
				return fmt.Errorf("upload failed: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode >= 400 {
				body, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("upload error %d: %s", resp.StatusCode, string(body))
			}

			PrintSuccess(fmt.Sprintf("Uploaded %s -> %s", localPath, remoteKey))
			return nil
		},
	}
}

func storageDownloadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "download <remote-key> <local-path>",
		Short: "Download a file from storage",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			if err := RequireProject(cfg); err != nil {
				return err
			}

			client, err := NewClientFromConfig()
			if err != nil {
				return err
			}

			resp, err := client.GetRaw("/platform/projects/" + cfg.ActiveProject + "/storage/" + args[0])
			if err != nil {
				return err
			}
			defer resp.Body.Close()

			outFile, err := os.Create(args[1])
			if err != nil {
				return fmt.Errorf("creating output file: %w", err)
			}
			defer outFile.Close()

			n, err := io.Copy(outFile, resp.Body)
			if err != nil {
				return fmt.Errorf("writing file: %w", err)
			}

			PrintSuccess(fmt.Sprintf("Downloaded %s (%s)", args[0], formatSize(n)))
			return nil
		},
	}
}

func storageDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <key>",
		Short: "Delete a file from storage",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			if err := RequireProject(cfg); err != nil {
				return err
			}

			client, err := NewClientFromConfig()
			if err != nil {
				return err
			}

			_, err = client.Delete("/platform/projects/" + cfg.ActiveProject + "/storage/" + args[0])
			if err != nil {
				return err
			}

			PrintSuccess(fmt.Sprintf("Deleted %s", args[0]))
			return nil
		},
	}
}

func storageUrlCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "url <key>",
		Short: "Generate a signed URL for a file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			if err := RequireProject(cfg); err != nil {
				return err
			}

			client, err := NewClientFromConfig()
			if err != nil {
				return err
			}

			expires, _ := cmd.Flags().GetInt("expires")

			body := map[string]interface{}{
				"key":        args[0],
				"expires_in": expires,
			}

			data, err := client.Post("/platform/projects/"+cfg.ActiveProject+"/storage/signed-url", body)
			if err != nil {
				return err
			}

			var result struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(data, &result); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			fmt.Println(result.URL)
			return nil
		},
	}
	cmd.Flags().IntP("expires", "e", 3600, "URL expiration in seconds")
	return cmd
}

func formatSize(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
