package functions

import (
	"errors"
	"fmt"
	"strings"

	esbuild "github.com/evanw/esbuild/pkg/api"
)

// MaxCodeBytes is the largest function source accepted on create and
// update.
const MaxCodeBytes = 1 << 20

// ErrCodeTooLarge is returned for source over MaxCodeBytes.
var ErrCodeTooLarge = errors.New("function code too large (max 1 MB)")

// transpileSlots bounds concurrent esbuild transforms per process, so a
// burst of deploys can't multiply their memory peak.
const transpileSlots = 2

var transpileSem = make(chan struct{}, transpileSlots)

// checkCodeSize refuses source over MaxCodeBytes.
func checkCodeSize(code string) error {
	if len(code) > MaxCodeBytes {
		return ErrCodeTooLarge
	}
	return nil
}

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
//
// esbuild needs roughly 60x the source size in memory while it works, so
// the source size is capped (MaxCodeBytes, checked by Create / Update)
// and at most transpileSlots transforms run at once per process.
func Transpile(source string) (string, error) {
	transpileSem <- struct{}{}
	defer func() { <-transpileSem }()
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
