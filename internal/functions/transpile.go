package functions

import (
	"fmt"
	"strings"

	esbuild "github.com/evanw/esbuild/pkg/api"
)

// Transpile converts user TypeScript/JavaScript source into the plain
// CommonJS-shaped JavaScript the runner's worker bootstrap can execute
// (closes #189).
//
// The runner loads code with `new Function()` inside a permissions-none
// worker — no TypeScript loader, no ES-module loader. esbuild bridges
// the gap at deploy time:
//
//   - LoaderTS strips type annotations (type-STRIP only — there is no
//     type CHECKING; a tsc error can still deploy fine)
//   - FormatCommonJS rewrites `export default` / named exports into
//     `module.exports`, which the bootstrap's handler detection picks up
//
// Plain JS passes through unchanged in shape, so pre-existing
// `globalThis.handler = ...` functions keep working.
//
// Third-party imports (`import x from "https://..."` / npm packages)
// compile to `require(...)` calls; the worker has no module loader, so
// the bootstrap's require stub rejects them at load time with a clear
// message. Bundling dependency graphs is out of scope here.
//
// Errors carry esbuild's line/column diagnostics so a bad deploy fails
// at deploy time with a pointer to the offending source, instead of an
// opaque failure on first invocation.
func Transpile(source string) (string, error) {
	result := esbuild.Transform(source, esbuild.TransformOptions{
		Loader:     esbuild.LoaderTS,
		Format:     esbuild.FormatCommonJS,
		Target:     esbuild.ES2022,
		Sourcefile: "function.ts",
		LogLevel:   esbuild.LogLevelSilent,
	})
	if len(result.Errors) > 0 {
		msgs := make([]string, 0, len(result.Errors))
		for _, e := range result.Errors {
			loc := ""
			if e.Location != nil {
				loc = fmt.Sprintf("line %d:%d: ", e.Location.Line, e.Location.Column)
			}
			msgs = append(msgs, loc+e.Text)
		}
		return "", fmt.Errorf("compile error: %s", strings.Join(msgs, "; "))
	}
	return string(result.Code), nil
}
