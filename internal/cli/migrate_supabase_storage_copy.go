package cli

// The copy behind `eurobase import supabase storage --apply`.
//
// For every file in the source's storage.objects, in batches:
//
//  1. ask the project for an upload URL (POST …/storage/import), which
//     records the file with its owner — or answers that it is already
//     there, so an interrupted copy picks up where it stopped;
//  2. stream the file from Supabase's storage API straight to that URL,
//     a few at a time; nothing is kept on disk;
//  3. tell the project which uploads finished (…/storage/import/
//     complete), which records each file's size from the bucket.
//
// A Supabase bucket becomes a folder: avatars/u1/me.png stays
// avatars/u1/me.png. A file keeps its owner, so the end user who uploaded
// it keeps the access they had. Files of a public bucket become developer
// files in a folder shared with everyone, because on Supabase anyone can
// read every file of a public bucket.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// sourceFile is one row of the source's storage.objects.
type sourceFile struct {
	Bucket      string
	Name        string
	Size        int64 // -1 when the source didn't record it
	ContentType string
	Owner       string
}

// Key is the file's key on Eurobase: the bucket is the top folder.
func (f sourceFile) Key() string { return f.Bucket + "/" + f.Name }

// importItem / importAnswer / completeAnswer mirror the platform API
// (internal/storage/import.go).
type importItem struct {
	Key         string `json:"key"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type,omitempty"`
	Owner       string `json:"owner,omitempty"`
}

type importAnswer struct {
	Key     string            `json:"key"`
	Status  string            `json:"status"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Code    string            `json:"code"`
	Error   string            `json:"error"`
}

type completeAnswer struct {
	Key    string `json:"key"`
	Status string `json:"status"`
	Size   int64  `json:"size"`
}

// storageTarget is the project's side of the copy.
type storageTarget interface {
	Import(items []importItem) ([]importAnswer, error)
	Complete(keys []string) ([]completeAnswer, error)
	Share(prefix, visibility string) error
	// SharedFolders returns the project's shared folders: prefix → visibility.
	SharedFolders() (map[string]string, error)
	// HasFiles reports whether the project has any file under prefix.
	HasFiles(prefix string) (bool, error)
}

// storageSource is where the bytes come from.
type storageSource interface {
	Open(ctx context.Context, f sourceFile) (io.ReadCloser, int64, error)
}

// copyOptions steer one run.
type copyOptions struct {
	Parallel int
	// MissingOwner: "developer" imports a file whose owner isn't a user
	// of the project as a developer file; "skip" leaves it out.
	MissingOwner string
	// PublicBuckets are the source buckets anyone could read.
	PublicBuckets map[string]bool
	Upload        *http.Client
	Progress      func(copyStats)
}

// copyStats counts what a run did.
type copyStats struct {
	Seen, Copied, Already, Skipped, Failed int64
	Bytes                                  int64
	Problems                               []string
	OwnerDropped                           int64
	SharedFolders                          []string
}

func (s *copyStats) problem(format string, args ...any) {
	if len(s.Problems) < 200 {
		s.Problems = append(s.Problems, fmt.Sprintf(format, args...))
	} else if len(s.Problems) == 200 {
		s.Problems = append(s.Problems, "… more not listed")
	}
}

// copyFiles copies one batch of files.
func copyFiles(ctx context.Context, files []sourceFile, src storageSource, dst storageTarget, opts copyOptions, stats *copyStats) error {
	items := make([]importItem, 0, len(files))
	byKey := map[string]sourceFile{}
	for _, f := range files {
		stats.Seen++
		// -1: the source doesn't know the size; the project then counts a
		// file that is there as copied.
		it := importItem{Key: f.Key(), Size: f.Size, ContentType: f.ContentType, Owner: f.Owner}
		if opts.PublicBuckets[f.Bucket] {
			// Everyone could read it: a developer file in a shared folder.
			if it.Owner != "" {
				stats.OwnerDropped++
			}
			it.Owner = ""
		}
		items = append(items, it)
		byKey[it.Key] = f
	}
	answers, err := dst.Import(items)
	if err != nil {
		return err
	}
	// A file whose owner isn't a user here: once more as a developer file.
	var retry []importItem
	for _, a := range answers {
		if a.Status == "error" && a.Code == "unknown_owner" && opts.MissingOwner == "developer" {
			f := byKey[a.Key]
			retry = append(retry, importItem{Key: a.Key, Size: f.Size, ContentType: f.ContentType})
		}
	}
	if len(retry) > 0 {
		again, err := dst.Import(retry)
		if err != nil {
			return err
		}
		replaced := map[string]importAnswer{}
		for _, a := range again {
			replaced[a.Key] = a
			if a.Status == "upload" || a.Status == "exists" {
				stats.OwnerDropped++
			}
		}
		for i, a := range answers {
			if r, ok := replaced[a.Key]; ok {
				answers[i] = r
			}
		}
	}

	var uploads []importAnswer
	for _, a := range answers {
		switch a.Status {
		case "upload":
			uploads = append(uploads, a)
		case "exists":
			stats.Already++
		case "conflict":
			stats.Skipped++
			stats.problem("%s: %s — left as it is", a.Key, a.Error)
		default:
			if a.Code == "unknown_owner" {
				stats.Skipped++
				stats.problem("%s: its owner is not a user of the project (import the users first, or pass --missing-owner developer)", a.Key)
				continue
			}
			stats.Failed++
			stats.problem("%s: %s", a.Key, a.Error)
		}
	}

	var (
		mu   sync.Mutex
		done []string
		wg   sync.WaitGroup
		sem  = make(chan struct{}, max(opts.Parallel, 1))
	)
	for _, a := range uploads {
		a := a
		f := byKey[a.Key]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			n, err := transferOne(ctx, src, f, a, opts.Upload)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				stats.Failed++
				stats.problem("%s: %v", a.Key, err)
				return
			}
			stats.Bytes += n
			done = append(done, a.Key)
		}()
	}
	wg.Wait()

	if len(done) > 0 {
		sort.Strings(done)
		results, err := dst.Complete(done)
		if err != nil {
			return err
		}
		if len(results) != len(done) {
			return fmt.Errorf("the project answered for %d of %d uploaded files", len(results), len(done))
		}
		for _, r := range results {
			if r.Status == "done" {
				stats.Copied++
			} else {
				stats.Failed++
				stats.problem("%s: uploaded, but the project doesn't see it in its bucket", r.Key)
			}
		}
	}
	if opts.Progress != nil {
		opts.Progress(*stats)
	}
	return nil
}

// transferOne streams one file from the source to its upload URL, with
// one more attempt when the connection fails.
func transferOne(ctx context.Context, src storageSource, f sourceFile, a importAnswer, client *http.Client) (int64, error) {
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		n, retryable, err := transferAttempt(ctx, src, f, a, client)
		if err == nil {
			return n, nil
		}
		last = err
		if !retryable || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return 0, last
}

// transferDeadline is how long one file may take: ten minutes, plus the
// time its size needs at 256 KB/s. A stalled connection ends there
// instead of holding the run.
func transferDeadline(size int64) time.Duration {
	if size < 0 {
		size = 0
	}
	return 10*time.Minute + time.Duration(size/(256<<10))*time.Second
}

func transferAttempt(ctx context.Context, src storageSource, f sourceFile, a importAnswer, client *http.Client) (int64, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, transferDeadline(f.Size))
	defer cancel()
	body, size, err := src.Open(ctx, f)
	if err != nil {
		return 0, true, fmt.Errorf("read from Supabase: %w", err)
	}
	defer body.Close()
	if size < 0 {
		size = f.Size
	}
	if size < 0 {
		return 0, false, errors.New("the source reports no size for this file, which an upload needs")
	}
	if f.Size >= 0 && size != f.Size {
		return 0, false, fmt.Errorf("the source serves %d bytes, its records say %d", size, f.Size)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, a.URL, body)
	if err != nil {
		return 0, false, err
	}
	req.ContentLength = size
	ct := f.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	req.Header.Set("Content-Type", ct)
	for k, v := range a.Headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, true, fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return 0, resp.StatusCode >= 500, fmt.Errorf("upload: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return size, false, nil
}

// ── the two sides over HTTP ─────────────────────────────────────────

// supabaseStorageSource reads files through Supabase's storage API with
// the project's service role key.
type supabaseStorageSource struct {
	base   string // https://<ref>.supabase.co
	key    string
	client *http.Client
}

func (s *supabaseStorageSource) Open(ctx context.Context, f sourceFile) (io.ReadCloser, int64, error) {
	parts := strings.Split(f.Name, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	u := strings.TrimRight(s.base, "/") + "/storage/v1/object/" + url.PathEscape(f.Bucket) + "/" + strings.Join(parts, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+s.key)
	req.Header.Set("apikey", s.key)
	// The bytes as stored: a compressed answer would change the length
	// (and Go would unpack it, losing the length altogether).
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		resp.Body.Close()
		return nil, 0, fmt.Errorf("HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return resp.Body, resp.ContentLength, nil
}

// apiStorageTarget is the project's import API.
type apiStorageTarget struct {
	client    *APIClient
	projectID string
}

func (t *apiStorageTarget) Import(items []importItem) ([]importAnswer, error) {
	data, err := t.client.Post("/platform/projects/"+t.projectID+"/storage/import", map[string]any{"objects": items})
	if err != nil {
		return nil, err
	}
	var out struct {
		Results []importAnswer `json:"results"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("import answer: %w", err)
	}
	if len(out.Results) != len(items) {
		return nil, fmt.Errorf("import answer has %d results for %d files", len(out.Results), len(items))
	}
	return out.Results, nil
}

func (t *apiStorageTarget) Complete(keys []string) ([]completeAnswer, error) {
	data, err := t.client.Post("/platform/projects/"+t.projectID+"/storage/import/complete", map[string]any{"keys": keys})
	if err != nil {
		return nil, err
	}
	var out struct {
		Results []completeAnswer `json:"results"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("complete answer: %w", err)
	}
	return out.Results, nil
}

func (t *apiStorageTarget) SharedFolders() (map[string]string, error) {
	data, err := t.client.Get("/platform/projects/" + t.projectID + "/storage-sharing")
	if err != nil {
		return nil, err
	}
	var rules []struct {
		Prefix     string `json:"prefix"`
		Visibility string `json:"visibility"`
	}
	if err := json.Unmarshal(data, &rules); err != nil {
		return nil, fmt.Errorf("shared folders: %w", err)
	}
	out := map[string]string{}
	for _, r := range rules {
		out[r.Prefix] = r.Visibility
	}
	return out, nil
}

func (t *apiStorageTarget) HasFiles(prefix string) (bool, error) {
	data, err := t.client.Get("/platform/projects/" + t.projectID + "/storage?limit=1&prefix=" + url.QueryEscape(prefix))
	if err != nil {
		return false, err
	}
	var out struct {
		Objects []json.RawMessage `json:"objects"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return false, fmt.Errorf("list files: %w", err)
	}
	return len(out.Objects) > 0, nil
}

func (t *apiStorageTarget) Share(prefix, visibility string) error {
	_, err := t.client.Put("/platform/projects/"+t.projectID+"/storage-sharing", map[string]string{"prefix": prefix, "visibility": visibility})
	return err
}
