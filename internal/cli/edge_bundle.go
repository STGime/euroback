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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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

	"github.com/eurobase/euroback/internal/netguard"
	esbuild "github.com/evanw/esbuild/pkg/api"
)

const (
	// maxFunctionCodeBytes mirrors the server's cap on a function's code.
	maxFunctionCodeBytes = 1 << 20
	// maxRemoteModuleBytes bounds one fetched module.
	maxRemoteModuleBytes = 8 << 20
	// maxRemoteModules bounds how many modules one build may fetch.
	maxRemoteModules = 2000
	// maxRemoteTotalBytes bounds everything one build may fetch.
	maxRemoteTotalBytes = 64 << 20
	remoteNamespace     = "eurobase-remote"
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
	// Compat is "supabase" when the function was built with the Supabase
	// compatibility layer.
	Compat string
	// EnvNames are the settings the function's own files read with
	// Deno.env.get / process.env, other than Supabase's own.
	EnvNames []string
	// UsesServiceRole: the code reads SUPABASE_SERVICE_ROLE_KEY, so its
	// client runs SQL with the service role.
	UsesServiceRole bool
}

// bundleOptions are for the CLI's own callers; the zero value is what a
// plain deploy uses.
type bundleOptions struct {
	// Virtual maps an import specifier to source code that is bundled in
	// its place (the CLI's own compatibility modules).
	Virtual map[string]string
	// Compat: "supabase" builds the function with the Supabase
	// compatibility layer (see edge_compat.go); "" builds it as it is.
	Compat string
	// loader, when set, is shared by several builds so each remote module
	// is downloaded once.
	loader *remoteLoader
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
	imap := loadImportMap(filepath.Dir(absEntry))
	// One loader for every build: a second build fetches nothing again.
	rl := opts.loader
	if rl == nil {
		rl = newRemoteLoader()
	}
	compat := opts.Compat == compatSupabase
	virtual := opts.Virtual
	if compat {
		virtual = map[string]string{}
		for k, v := range opts.Virtual {
			virtual[k] = v
		}
		for k, v := range supabaseCompatModules() {
			virtual[k] = v
		}
	}
	// The bundle runs inside a function of its own, so nothing it
	// declares at the top (a helper called "handler", a variable called
	// "exports") meets the names the runner uses.
	banner, footer := "(() => {", "})();"

	build := func(minify bool) esbuild.BuildResult {
		return esbuild.Build(esbuild.BuildOptions{
			EntryPoints:      []string{absEntry},
			AbsWorkingDir:    filepath.Dir(absEntry),
			Bundle:           true,
			Write:            false,
			Metafile:         true,
			Format:           esbuild.FormatCommonJS,
			Platform:         esbuild.PlatformNeutral,
			Target:           esbuild.ES2022,
			MainFields:       []string{"module", "main"},
			Conditions:       []string{"deno", "worker", "browser", "import", "default"},
			LogLevel:         esbuild.LogLevelSilent,
			MinifyWhitespace: minify,
			MinifySyntax:     minify,
			LegalComments:    esbuild.LegalCommentsNone,
			// The server compiles every function as TypeScript, whatever
			// the file is called; read local files the same way.
			Loader:            map[string]esbuild.Loader{".js": esbuild.LoaderTS, ".mjs": esbuild.LoaderTS, ".cjs": esbuild.LoaderTS},
			Banner:            map[string]string{"js": banner},
			Footer:            map[string]string{"js": footer},
			ResolveExtensions: []string{".ts", ".tsx", ".mts", ".js", ".mjs", ".jsx", ".json"},
			Plugins:           []esbuild.Plugin{resolverPlugin(rl, imap, virtual, compat)},
		})
	}

	res := build(false)
	if len(res.Errors) > 0 {
		return nil, bundleError(res.Errors)
	}
	local, remote := bundleInputs(res.Metafile, filepath.Base(absEntry))
	if !compat && len(local) == 0 && len(remote) == 0 {
		// Nothing was imported: deploy the developer's own text.
		return &bundleResult{Code: string(source)}, nil
	}
	if len(res.OutputFiles) != 1 {
		return nil, fmt.Errorf("bundling produced %d files, want 1", len(res.OutputFiles))
	}
	out := &bundleResult{Bundled: true, Local: local, Remote: remote}
	out.EnvNames, out.UsesServiceRole = envNamesRead(string(source), filepath.Dir(absEntry), local)
	if compat {
		// Built again inside the wrapper that gives the code what it had
		// on Supabase; the wrapper needs the names found above.
		out.Compat = compatSupabase
		banner, footer = supabaseCompatWrapper(out.EnvNames)
		res = build(false)
		if len(res.Errors) > 0 {
			return nil, bundleError(res.Errors)
		}
	}
	code := res.OutputFiles[0].Contents
	// A handler that is only a top-level `function handler` is found by
	// the runner in a single file, but not inside a bundle.
	if text := string(code); !compat && !strings.Contains(text, "module.exports") && !strings.Contains(text, "exports.default") && !strings.Contains(text, "globalThis.handler") {
		return nil, errors.New("the function doesn't export its handler — write `export default async (req, ctx) => { … }` (a function with imports needs the export)")
	}
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
	if err := checkReadsTheSameAsTypeScript(string(code)); err != nil {
		return nil, err
	}
	out.Code = header + string(code)
	return out, nil
}

// checkReadsTheSameAsTypeScript refuses a bundle the server would compile
// into something else. The server reads every function as TypeScript, and
// a few JavaScript expressions mean something different there (`a < b >
// (c)` is a generic call). Library code is JavaScript, so compile it both
// ways and compare.
func checkReadsTheSameAsTypeScript(code string) error {
	compile := func(loader esbuild.Loader) (string, bool) {
		r := esbuild.Transform(code, esbuild.TransformOptions{Loader: loader, Format: esbuild.FormatCommonJS, Target: esbuild.ES2022, LogLevel: esbuild.LogLevelSilent})
		return string(r.Code), len(r.Errors) == 0
	}
	asJS, okJS := compile(esbuild.LoaderJS)
	asTS, okTS := compile(esbuild.LoaderTS)
	if !okTS {
		return errors.New("the bundled code doesn't compile as TypeScript, which is how the server reads a function — one of the imported libraries uses syntax that only works as plain JavaScript")
	}
	if okJS && asJS != asTS {
		return errors.New("one of the imported libraries contains JavaScript that means something else when read as TypeScript (which is how the server reads a function), so the deployed function would not behave like the library — use another build of that library")
	}
	return nil
}

// bundleHeader tells whoever opens the function in the console where its
// code came from. Only the last two parts of the path: an absolute path
// would put the developer's folder names into the stored code.
func bundleHeader(entryPath string, modules int) string {
	p := filepath.ToSlash(entryPath)
	if parts := strings.Split(p, "/"); len(parts) > 2 {
		p = strings.Join(parts[len(parts)-2:], "/")
	}
	return fmt.Sprintf("// Bundled by the eurobase CLI from %s with %d imported module(s).\n// Edit that file and deploy again: changes made here are lost on the next deploy.\n",
		commentSafe(p), modules)
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
			file := strings.TrimPrefix(m.Location.File, remoteNamespace+":")
			loc = fmt.Sprintf("%s:%d:%d: ", file, m.Location.Line, m.Location.Column)
		}
		lines = append(lines, loc+m.Text)
	}
	return errors.New("can't bundle the function:\n  " + strings.Join(lines, "\n  ") + "\n(--no-bundle deploys the file as it is, without its imports)")
}

// ── resolving ───────────────────────────────────────────────────────

const virtualNamespace = "eurobase-virtual"

var (
	remoteSpecRe  = regexp.MustCompile(`^https?://`)
	npmSpecRe     = regexp.MustCompile(`^(npm|jsr):`)
	nodeSpecRe    = regexp.MustCompile(`^node:`)
	otherSchemeRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`)
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

// nestedResolve marks the plugin's own b.Resolve call, which comes back
// through the plugin.
type nestedResolve struct{ nested bool }

func resolverPlugin(rl *remoteLoader, imap *importMap, virtual map[string]string, compat bool) esbuild.Plugin {
	return esbuild.Plugin{
		Name: "eurobase-imports",
		Setup: func(b esbuild.PluginBuild) {
			// resolveRemote fetches the module now, so the path esbuild
			// keeps is the address after redirects: relative imports
			// inside it resolve against where it really is, and one module
			// reached by two addresses is bundled once.
			resolveRemote := func(u string, a esbuild.OnResolveArgs) (esbuild.OnResolveResult, error) {
				if a.Namespace != remoteNamespace {
					// Written by the developer (in a file or the import
					// map): this host is theirs to name, private or not.
					rl.trust(u)
				}
				m, err := rl.fetch(u)
				if err != nil {
					if a.Kind == esbuild.ResolveJSRequireCall {
						// An optional dependency (require in try/catch):
						// leave the call, it throws when it runs.
						return esbuild.OnResolveResult{Path: a.Path, External: true}, nil
					}
					return esbuild.OnResolveResult{}, err
				}
				return esbuild.OnResolveResult{Path: m.finalURL, Namespace: remoteNamespace}, nil
			}
			// unresolved is for a name nothing can supply.
			unresolved := func(a esbuild.OnResolveArgs, msg string) (esbuild.OnResolveResult, bool, error) {
				if a.Kind == esbuild.ResolveJSRequireCall {
					return esbuild.OnResolveResult{Path: a.Path, External: true}, true, nil
				}
				return esbuild.OnResolveResult{}, true, errors.New(msg)
			}
			// resolveSpec handles everything that isn't a plain relative
			// path between local files. handled=false leaves it to esbuild.
			var resolveSpec func(spec string, a esbuild.OnResolveArgs, depth int) (esbuild.OnResolveResult, bool, error)
			resolveSpec = func(spec string, a esbuild.OnResolveArgs, depth int) (esbuild.OnResolveResult, bool, error) {
				if _, ok := virtual[spec]; ok {
					return esbuild.OnResolveResult{Path: spec, Namespace: virtualNamespace}, true, nil
				}
				if compat {
					if alias := supabaseCompatAlias(spec); alias != "" {
						return esbuild.OnResolveResult{Path: alias, Namespace: virtualNamespace}, true, nil
					}
				}
				switch {
				case nodeSpecRe.MatchString(spec):
					shown := a.Importer
					if a.Namespace != remoteNamespace && shown != "" {
						shown = filepath.Base(shown)
					}
					return unresolved(a, nodeBuiltinMessage(spec, shown))
				case remoteSpecRe.MatchString(spec):
					r, err := resolveRemote(spec, a)
					return r, true, err
				case npmSpecRe.MatchString(spec):
					r, err := resolveRemote(cdnURL(spec), a)
					return r, true, err
				case strings.HasPrefix(spec, "data:"):
					return esbuild.OnResolveResult{}, false, nil
				case otherSchemeRe.MatchString(spec) && !filepath.IsAbs(spec):
					return unresolved(a, fmt.Sprintf("can't import %q: only https://, npm:, jsr: and relative paths can be bundled", spec))
				}
				if a.Namespace == remoteNamespace {
					// Inside a fetched module: paths are relative to its URL.
					if strings.HasPrefix(spec, "/") || strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") {
						base, err := url.Parse(a.Importer)
						if err != nil {
							return esbuild.OnResolveResult{}, true, err
						}
						ref, err := url.Parse(spec)
						if err != nil {
							return esbuild.OnResolveResult{}, true, err
						}
						r, err := resolveRemote(base.ResolveReference(ref).String(), a)
						return r, true, err
					}
					// A bare name inside a fetched module is an npm package.
					r, err := resolveRemote(npmCDN+spec, a)
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
						r := b.Resolve("./"+filepath.Base(p), esbuild.ResolveOptions{ResolveDir: filepath.Dir(p), Kind: esbuild.ResolveJSImportStatement, PluginData: nestedResolve{nested: true}})
						if len(r.Errors) > 0 {
							return esbuild.OnResolveResult{}, true, fmt.Errorf("import map entry for %q points to %s, which doesn't exist", spec, p)
						}
						return esbuild.OnResolveResult{Path: r.Path}, true, nil
					}
					return resolveSpec(target, a, depth+1)
				}
				msg := fmt.Sprintf("can't resolve %q: import it by URL (https://…), as npm:%s, by relative path, or add it to the \"imports\" of a deno.json next to the function", spec, spec)
				if imap.err != nil {
					msg += fmt.Sprintf(" (an import map was found but can't be read: %v)", imap.err)
				}
				return unresolved(a, msg)
			}

			b.OnResolve(esbuild.OnResolveOptions{Filter: `.*`}, func(a esbuild.OnResolveArgs) (esbuild.OnResolveResult, error) {
				if a.Kind == esbuild.ResolveEntryPoint {
					return esbuild.OnResolveResult{}, nil
				}
				if n, ok := a.PluginData.(nestedResolve); ok && n.nested {
					return esbuild.OnResolveResult{}, nil
				}
				r, handled, err := resolveSpec(a.Path, a, 0)
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

// ── fetching ────────────────────────────────────────────────────────

type remoteModule struct {
	finalURL string // after redirects
	body     string
	loader   esbuild.Loader
}

// remoteEntry is one address being fetched, or fetched.
type remoteEntry struct {
	once sync.Once
	m    *remoteModule
	err  error
}

// remoteLoader fetches modules for one deploy.
//
// A module fetched from the internet decides what it imports next. It
// must not be able to make the CLI fetch from the developer's own machine
// or network and carry the answer into the bundle, so connections go to
// public addresses only — except to a host the developer named themselves
// in a local file or the import map (a module server on localhost, say).
// The check is made when connecting, so it covers redirects.
type remoteLoader struct {
	client  *http.Client
	mu      sync.Mutex
	entries map[string]*remoteEntry
	trusted map[string]bool // host:port named by the developer
	count   int
	bytes   int64
}

func newRemoteLoader() *remoteLoader {
	rl := &remoteLoader{entries: map[string]*remoteEntry{}, trusted: map[string]bool{}}
	plain := &net.Dialer{Timeout: 15 * time.Second}
	rl.client = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				rl.mu.Lock()
				ok := rl.trusted[strings.ToLower(addr)]
				rl.mu.Unlock()
				if ok {
					return plain.DialContext(ctx, network, addr)
				}
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				return netguard.DialPublic(ctx, host, port)
			},
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			MaxIdleConnsPerHost:   8,
		},
	}
	return rl
}

// trust records the host of a URL the developer wrote.
func (rl *remoteLoader) trust(rawURL string) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	rl.mu.Lock()
	rl.trusted[strings.ToLower(net.JoinHostPort(u.Hostname(), port))] = true
	rl.mu.Unlock()
}

// fetch returns the module at rawURL, fetching it once however many
// imports name it.
func (rl *remoteLoader) fetch(rawURL string) (*remoteModule, error) {
	rl.mu.Lock()
	e, ok := rl.entries[rawURL]
	if !ok {
		e = &remoteEntry{}
		rl.entries[rawURL] = e
	}
	rl.mu.Unlock()
	e.once.Do(func() {
		e.m, e.err = rl.download(rawURL)
		if e.err == nil && e.m.finalURL != rawURL {
			// Known under the address it redirected to as well.
			rl.mu.Lock()
			if _, exists := rl.entries[e.m.finalURL]; !exists {
				done := &remoteEntry{m: e.m}
				done.once.Do(func() {})
				rl.entries[e.m.finalURL] = done
			}
			rl.mu.Unlock()
		}
	})
	return e.m, e.err
}

func (rl *remoteLoader) download(rawURL string) (*remoteModule, error) {
	rl.mu.Lock()
	rl.count++
	n, total := rl.count, rl.bytes
	rl.mu.Unlock()
	if n > maxRemoteModules {
		return nil, fmt.Errorf("more than %d remote modules: import a bundled build of the library (on esm.sh, add ?bundle to the URL)", maxRemoteModules)
	}
	if total > maxRemoteTotalBytes {
		return nil, fmt.Errorf("more than %d MB of remote modules: import a smaller build of the library", maxRemoteTotalBytes>>20)
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
	rl.mu.Lock()
	rl.bytes += int64(len(body))
	rl.mu.Unlock()
	return &remoteModule{
		finalURL: resp.Request.URL.String(),
		body:     string(body),
		loader:   remoteLoaderFor(resp.Request.URL.Path, resp.Header.Get("Content-Type")),
	}, nil
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
// up to three folders above it (supabase/functions/<name>/ → project),
// not past the top of the repository.
type importMap struct {
	dir     string
	imports map[string]string
	// err is why a file that was found couldn't be used. It only matters
	// to a function that needs the map, so it is reported with the name
	// that couldn't be resolved — a function without such imports deploys.
	err error
}

func loadImportMap(startDir string) *importMap {
	parse := func(p string, into any) error {
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(relaxJSON(raw), into); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		return nil
	}
	dir := startDir
	for i := 0; i < 4; i++ {
		for _, name := range []string{"deno.json", "deno.jsonc", "import_map.json"} {
			p := filepath.Join(dir, name)
			if _, err := os.Stat(p); err != nil {
				continue
			}
			var doc struct {
				Imports   map[string]string `json:"imports"`
				ImportMap string            `json:"importMap"`
			}
			if err := parse(p, &doc); err != nil {
				return &importMap{err: err}
			}
			if doc.Imports == nil && doc.ImportMap != "" {
				ref := filepath.Join(dir, filepath.FromSlash(doc.ImportMap))
				if err := parse(ref, &doc); err != nil {
					return &importMap{err: err}
				}
				return &importMap{dir: filepath.Dir(ref), imports: doc.Imports}
			}
			if len(doc.Imports) > 0 {
				return &importMap{dir: dir, imports: doc.Imports}
			}
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return &importMap{}
}

// lookup returns what spec maps to and the folder a relative target is
// relative to: an exact entry, the longest "prefix/" entry, or a path
// below a package entry ("@std/encoding/base64" with "@std/encoding"
// mapped to a jsr: or npm: package, as `deno add` writes it).
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
	if best != "" {
		return m.imports[best] + strings.TrimPrefix(spec, best), m.dir, true
	}
	for k, t := range m.imports {
		if npmSpecRe.MatchString(t) && strings.HasPrefix(spec, k+"/") && len(k) > len(best) {
			best = k
		}
	}
	if best != "" {
		return strings.TrimSuffix(m.imports[best], "/") + strings.TrimPrefix(spec, best), m.dir, true
	}
	return "", "", false
}

// relaxJSON removes // and /* */ comments and trailing commas outside
// strings, so deno.jsonc parses.
func relaxJSON(in []byte) []byte {
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
		if c == '}' || c == ']' {
			// Drop a comma that only whitespace separates from here.
			j := len(out) - 1
			for j >= 0 && (out[j] == ' ' || out[j] == '\t' || out[j] == '\n' || out[j] == '\r') {
				j--
			}
			if j >= 0 && out[j] == ',' {
				out = append(out[:j], out[j+1:]...)
			}
		}
		out = append(out, c)
	}
	return out
}
