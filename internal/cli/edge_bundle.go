package cli

// Bundling for `eurobase edge-functions deploy`.
//
// The runner loads one script per function and has no module loader, so
// a function that imports anything has to arrive as a single file. The
// CLI builds that file here, on the developer's machine, with esbuild:
//
//   - relative imports (./util.ts, ../_shared/cors.ts)
//   - https:// imports, fetched once per build (esm.sh, deno.land, …)
//   - npm: and jsr: specifiers, served through esm.sh
//   - bare names mapped by an import map (deno.json / import_map.json)
//
// A function that imports nothing is deployed exactly as written, so the
// console keeps showing the developer's own source.
//
// What can't be bundled is refused with the reason: Node built-ins
// (node:fs, …) don't exist in the runner, and a bare name with no import
// map entry has nothing to resolve to.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	esbuild "github.com/evanw/esbuild/pkg/api"
)

const (
	// maxFunctionCodeBytes mirrors the server's cap on a function's code.
	maxFunctionCodeBytes = 1 << 20
	// maxRemoteModuleBytes bounds one fetched module.
	maxRemoteModuleBytes = 8 << 20
	// maxRemoteModules bounds how many modules one build may fetch.
	maxRemoteModules = 2000
	remoteNamespace  = "eurobase-remote"
	// npmCDN serves npm: and jsr: specifiers as ES modules.
	npmCDN = "https://esm.sh/"
)

// bundleResult is what bundleFunction produced.
type bundleResult struct {
	// Code is what to deploy.
	Code string
	// Bundled is false when the entry file imports nothing and Code is the
	// file as written.
	Bundled bool
	// Local and Remote are the modules that went into the bundle besides
	// the entry file.
	Local  []string
	Remote []string
	// Minified is set when the bundle only fits the size cap minified.
	Minified bool
}

// bundleOptions are for tests; the zero value is what the command uses.
type bundleOptions struct {
	// Virtual maps an import specifier to source code that is bundled in
	// its place (the CLI's own compatibility modules).
	Virtual map[string]string
	// HTTPClient fetches remote modules. Nil: a client with a timeout.
	HTTPClient *http.Client
}

// bundleFunction builds the single file to deploy for entryPath.
func bundleFunction(entryPath string, opts bundleOptions) (*bundleResult, error) {
	source, err := os.ReadFile(entryPath)
	if err != nil {
		return nil, fmt.Errorf("reading file: %w", err)
	}
	absEntry, err := filepath.Abs(entryPath)
	if err != nil {
		return nil, err
	}
	// The real path, so the build's file names are relative to the
	// function's folder whatever links lead to it.
	if real, err := filepath.EvalSymlinks(absEntry); err == nil {
		absEntry = real
	}
	imap, err := loadImportMap(filepath.Dir(absEntry))
	if err != nil {
		return nil, err
	}

	build := func(minify bool) esbuild.BuildResult {
		rl := &remoteLoader{client: opts.HTTPClient, fetched: map[string]*remoteModule{}}
		if rl.client == nil {
			rl.client = &http.Client{Timeout: 30 * time.Second}
		}
		res := esbuild.Build(esbuild.BuildOptions{
			EntryPoints:       []string{absEntry},
			AbsWorkingDir:     filepath.Dir(absEntry),
			Bundle:            true,
			Write:             false,
			Metafile:          true,
			Format:            esbuild.FormatCommonJS,
			Platform:          esbuild.PlatformNeutral,
			Target:            esbuild.ES2022,
			MainFields:        []string{"module", "main"},
			Conditions:        []string{"deno", "worker", "browser", "import", "default"},
			LogLevel:          esbuild.LogLevelSilent,
			MinifyWhitespace:  minify,
			MinifySyntax:      minify,
			LegalComments:     esbuild.LegalCommentsNone,
			ResolveExtensions: []string{".ts", ".tsx", ".mts", ".js", ".mjs", ".jsx", ".json"},
			Plugins:           []esbuild.Plugin{resolverPlugin(rl, imap, opts.Virtual)},
		})
		return res
	}

	res := build(false)
	if len(res.Errors) > 0 {
		return nil, bundleError(res.Errors)
	}
	local, remote := bundleInputs(res.Metafile, filepath.Base(absEntry))
	if len(local) == 0 && len(remote) == 0 {
		// Nothing was imported: deploy the developer's own text.
		return &bundleResult{Code: string(source)}, nil
	}
	if len(res.OutputFiles) != 1 {
		return nil, fmt.Errorf("bundling produced %d files, want 1", len(res.OutputFiles))
	}
	out := &bundleResult{Bundled: true, Local: local, Remote: remote}
	code := res.OutputFiles[0].Contents
	header := bundleHeader(entryPath, len(local)+len(remote))
	if len(header)+len(code) > maxFunctionCodeBytes {
		unminified := len(code)
		res = build(true)
		if len(res.Errors) > 0 {
			return nil, bundleError(res.Errors)
		}
		code = res.OutputFiles[0].Contents
		out.Minified = true
		if len(header)+len(code) > maxFunctionCodeBytes {
			return nil, fmt.Errorf("the function with its imports is %d KB (%d KB minified); a function's code can be up to %d KB — import less, or a smaller build of the library",
				unminified/1024, len(code)/1024, maxFunctionCodeBytes/1024)
		}
	}
	out.Code = header + string(code)
	return out, nil
}

// bundleHeader tells whoever opens the function in the console where its
// code came from.
func bundleHeader(entryPath string, modules int) string {
	return fmt.Sprintf("// Bundled by the eurobase CLI from %s with %d imported module(s).\n// Edit that file and deploy again: changes made here are lost on the next deploy.\n",
		commentSafe(filepath.ToSlash(entryPath)), modules)
}

// bundleInputs lists what the build read besides the entry file. Names of
// local files are relative to the function's folder.
func bundleInputs(metafile, entryName string) (local, remote []string) {
	var meta struct {
		Inputs map[string]json.RawMessage `json:"inputs"`
	}
	if err := json.Unmarshal([]byte(metafile), &meta); err != nil {
		return nil, nil
	}
	for name := range meta.Inputs {
		switch {
		case strings.HasPrefix(name, remoteNamespace+":"):
			remote = append(remote, strings.TrimPrefix(name, remoteNamespace+":"))
		case strings.HasPrefix(name, virtualNamespace+":"):
			local = append(local, strings.TrimPrefix(name, virtualNamespace+":"))
		default:
			if name != entryName {
				local = append(local, filepath.ToSlash(name))
			}
		}
	}
	sort.Strings(local)
	sort.Strings(remote)
	return local, remote
}

func bundleError(msgs []esbuild.Message) error {
	var lines []string
	for i, m := range msgs {
		if i == 8 {
			lines = append(lines, fmt.Sprintf("… and %d more", len(msgs)-i))
			break
		}
		loc := ""
		if m.Location != nil {
			file := m.Location.File
			if strings.HasPrefix(file, remoteNamespace+":") {
				file = strings.TrimPrefix(file, remoteNamespace+":")
			}
			loc = fmt.Sprintf("%s:%d:%d: ", file, m.Location.Line, m.Location.Column)
		}
		lines = append(lines, loc+m.Text)
	}
	return errors.New("can't bundle the function:\n  " + strings.Join(lines, "\n  "))
}

// ── resolving ───────────────────────────────────────────────────────

const virtualNamespace = "eurobase-virtual"

var (
	remoteSpecRe = regexp.MustCompile(`^https?://`)
	npmSpecRe    = regexp.MustCompile(`^(npm|jsr):`)
	nodeSpecRe   = regexp.MustCompile(`^node:`)
)

// nodeBuiltinMessage is the refusal for Node's own modules.
func nodeBuiltinMessage(spec, importer string) string {
	from := ""
	if importer != "" {
		from = " (imported by " + importer + ")"
	}
	return fmt.Sprintf("%q is a Node.js built-in%s; edge functions run without Node — use a build of the library for browsers or Deno, or the web API (fetch, crypto.subtle, …)", spec, from)
}

// cdnURL turns npm:name@1 or jsr:@scope/name@1 into its esm.sh address.
func cdnURL(spec string) string {
	if rest, ok := strings.CutPrefix(spec, "npm:"); ok {
		return npmCDN + strings.TrimPrefix(rest, "/")
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(spec, "jsr:"), "/")
	return npmCDN + "jsr/" + rest
}

func resolverPlugin(rl *remoteLoader, imap *importMap, virtual map[string]string) esbuild.Plugin {
	return esbuild.Plugin{
		Name: "eurobase-imports",
		Setup: func(b esbuild.PluginBuild) {
			resolveRemote := func(u string) (esbuild.OnResolveResult, error) {
				return esbuild.OnResolveResult{Path: u, Namespace: remoteNamespace}, nil
			}
			// resolveSpec handles everything that isn't a plain relative
			// path between local files. ok=false leaves it to esbuild.
			var resolveSpec func(spec, importer, namespace string, depth int) (esbuild.OnResolveResult, bool, error)
			resolveSpec = func(spec, importer, namespace string, depth int) (esbuild.OnResolveResult, bool, error) {
				if _, ok := virtual[spec]; ok {
					return esbuild.OnResolveResult{Path: spec, Namespace: virtualNamespace}, true, nil
				}
				switch {
				case nodeSpecRe.MatchString(spec):
					shown := importer
					if namespace != remoteNamespace && shown != "" {
						shown = filepath.Base(shown)
					}
					return esbuild.OnResolveResult{}, true, errors.New(nodeBuiltinMessage(spec, shown))
				case remoteSpecRe.MatchString(spec):
					r, err := resolveRemote(spec)
					return r, true, err
				case npmSpecRe.MatchString(spec):
					r, err := resolveRemote(cdnURL(spec))
					return r, true, err
				}
				if namespace == remoteNamespace {
					// Inside a fetched module: paths are relative to its URL.
					if strings.HasPrefix(spec, "/") || strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") {
						base, err := url.Parse(importer)
						if err != nil {
							return esbuild.OnResolveResult{}, true, err
						}
						ref, err := url.Parse(spec)
						if err != nil {
							return esbuild.OnResolveResult{}, true, err
						}
						r, err := resolveRemote(base.ResolveReference(ref).String())
						return r, true, err
					}
					// A bare name inside a fetched module is an npm package.
					r, err := resolveRemote(npmCDN + spec)
					return r, true, err
				}
				if strings.HasPrefix(spec, ".") || filepath.IsAbs(spec) {
					return esbuild.OnResolveResult{}, false, nil
				}
				// A bare name in a local file: only an import map gives it
				// a meaning.
				if target, base, ok := imap.lookup(spec); ok && depth < 5 {
					if strings.HasPrefix(target, ".") || strings.HasPrefix(target, "/") {
						p := target
						if !filepath.IsAbs(p) {
							p = filepath.Join(base, filepath.FromSlash(target))
						}
						r := b.Resolve("./"+filepath.Base(p), esbuild.ResolveOptions{ResolveDir: filepath.Dir(p), Kind: esbuild.ResolveJSImportStatement, PluginData: skipResolve})
						if len(r.Errors) > 0 {
							return esbuild.OnResolveResult{}, true, fmt.Errorf("import map entry for %q points to %s, which doesn't exist", spec, p)
						}
						return esbuild.OnResolveResult{Path: r.Path}, true, nil
					}
					return resolveSpec(target, importer, namespace, depth+1)
				}
				return esbuild.OnResolveResult{}, true, fmt.Errorf("can't resolve %q: import it by URL (https://…), as npm:%s, by relative path, or add it to the \"imports\" of a deno.json next to the function", spec, spec)
			}

			b.OnResolve(esbuild.OnResolveOptions{Filter: `.*`}, func(a esbuild.OnResolveArgs) (esbuild.OnResolveResult, error) {
				if a.Kind == esbuild.ResolveEntryPoint || a.PluginData == skipResolve {
					return esbuild.OnResolveResult{}, nil
				}
				r, handled, err := resolveSpec(a.Path, a.Importer, a.Namespace, 0)
				if err != nil {
					return esbuild.OnResolveResult{}, err
				}
				if !handled {
					return esbuild.OnResolveResult{}, nil
				}
				return r, nil
			})
			b.OnLoad(esbuild.OnLoadOptions{Filter: `.*`, Namespace: remoteNamespace}, func(a esbuild.OnLoadArgs) (esbuild.OnLoadResult, error) {
				m, err := rl.fetch(a.Path)
				if err != nil {
					return esbuild.OnLoadResult{}, err
				}
				return esbuild.OnLoadResult{Contents: &m.body, Loader: m.loader}, nil
			})
			b.OnLoad(esbuild.OnLoadOptions{Filter: `.*`, Namespace: virtualNamespace}, func(a esbuild.OnLoadArgs) (esbuild.OnLoadResult, error) {
				src := virtual[a.Path]
				return esbuild.OnLoadResult{Contents: &src, Loader: esbuild.LoaderTS}, nil
			})
		},
	}
}

// skipResolve marks a nested Resolve call so the plugin doesn't handle it
// again. (b.Resolve from inside onResolve re-enters the plugin chain.)
var skipResolve = &struct{}{}

// ── fetching ────────────────────────────────────────────────────────

type remoteModule struct {
	body   string
	loader esbuild.Loader
}

type remoteLoader struct {
	client  *http.Client
	mu      sync.Mutex
	fetched map[string]*remoteModule
}

func (rl *remoteLoader) fetch(rawURL string) (*remoteModule, error) {
	rl.mu.Lock()
	if m, ok := rl.fetched[rawURL]; ok {
		rl.mu.Unlock()
		return m, nil
	}
	n := len(rl.fetched)
	rl.mu.Unlock()
	if n >= maxRemoteModules {
		return nil, fmt.Errorf("more than %d remote modules: import a bundled build of the library (on esm.sh, add ?bundle to the URL)", maxRemoteModules)
	}

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	// A plain client name: module hosts pick the build by User-Agent, and
	// nothing about the developer or the project belongs in the request.
	req.Header.Set("User-Agent", "eurobase-cli")
	req.Header.Set("Accept", "application/javascript, application/typescript, text/javascript, */*")
	resp, err := rl.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: HTTP %d", rawURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRemoteModuleBytes+1))
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	if len(body) > maxRemoteModuleBytes {
		return nil, fmt.Errorf("fetch %s: larger than %d MB", rawURL, maxRemoteModuleBytes>>20)
	}
	m := &remoteModule{body: string(body), loader: remoteLoaderFor(resp.Request.URL.Path, resp.Header.Get("Content-Type"))}
	rl.mu.Lock()
	rl.fetched[rawURL] = m
	rl.mu.Unlock()
	return m, nil
}

// remoteLoaderFor picks how to parse a fetched module: by what the server
// says it is, then by its file name.
func remoteLoaderFor(urlPath, contentType string) esbuild.Loader {
	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "typescript"):
		if strings.HasSuffix(urlPath, ".tsx") {
			return esbuild.LoaderTSX
		}
		return esbuild.LoaderTS
	case strings.Contains(ct, "json"):
		return esbuild.LoaderJSON
	case strings.Contains(ct, "javascript"):
		if strings.HasSuffix(urlPath, ".jsx") {
			return esbuild.LoaderJSX
		}
		return esbuild.LoaderJS
	}
	switch path.Ext(urlPath) {
	case ".ts", ".mts":
		return esbuild.LoaderTS
	case ".tsx":
		return esbuild.LoaderTSX
	case ".jsx":
		return esbuild.LoaderJSX
	case ".json":
		return esbuild.LoaderJSON
	}
	return esbuild.LoaderJS
}

// ── import maps ─────────────────────────────────────────────────────

// importMap is the "imports" object of the nearest deno.json /
// deno.jsonc / import_map.json, looked for in the function's folder and
// up to three folders above it (supabase/functions/<name>/ → project).
type importMap struct {
	dir     string
	imports map[string]string
}

func loadImportMap(startDir string) (*importMap, error) {
	dir := startDir
	for i := 0; i < 4; i++ {
		for _, name := range []string{"deno.json", "deno.jsonc", "import_map.json"} {
			p := filepath.Join(dir, name)
			raw, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			var doc struct {
				Imports   map[string]string `json:"imports"`
				ImportMap string            `json:"importMap"`
			}
			if err := json.Unmarshal(stripJSONComments(raw), &doc); err != nil {
				return nil, fmt.Errorf("%s: %w", p, err)
			}
			if doc.Imports == nil && doc.ImportMap != "" {
				ref := filepath.Join(dir, filepath.FromSlash(doc.ImportMap))
				raw, err := os.ReadFile(ref)
				if err != nil {
					return nil, fmt.Errorf("%s names import map %s: %w", p, doc.ImportMap, err)
				}
				if err := json.Unmarshal(stripJSONComments(raw), &doc); err != nil {
					return nil, fmt.Errorf("%s: %w", ref, err)
				}
				return &importMap{dir: filepath.Dir(ref), imports: doc.Imports}, nil
			}
			if len(doc.Imports) > 0 {
				return &importMap{dir: dir, imports: doc.Imports}, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return &importMap{}, nil
}

// lookup returns what spec maps to and the folder a relative target is
// relative to: an exact entry, or the longest "prefix/" entry.
func (m *importMap) lookup(spec string) (target, baseDir string, ok bool) {
	if m == nil || len(m.imports) == 0 {
		return "", "", false
	}
	if t, ok := m.imports[spec]; ok {
		return t, m.dir, true
	}
	best := ""
	for k := range m.imports {
		if strings.HasSuffix(k, "/") && strings.HasPrefix(spec, k) && len(k) > len(best) {
			best = k
		}
	}
	if best == "" {
		return "", "", false
	}
	return m.imports[best] + strings.TrimPrefix(spec, best), m.dir, true
}

// stripJSONComments removes // and /* */ comments outside strings, so
// deno.jsonc parses.
func stripJSONComments(in []byte) []byte {
	out := make([]byte, 0, len(in))
	inString, escaped := false, false
	for i := 0; i < len(in); i++ {
		c := in[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == '/' && i+1 < len(in) && in[i+1] == '/' {
			for i < len(in) && in[i] != '\n' {
				i++
			}
			out = append(out, '\n')
			continue
		}
		if c == '/' && i+1 < len(in) && in[i+1] == '*' {
			i += 2
			for i+1 < len(in) && !(in[i] == '*' && in[i+1] == '/') {
				i++
			}
			i++
			continue
		}
		out = append(out, c)
	}
	return out
}
