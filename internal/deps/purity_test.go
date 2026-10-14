// Copied from an earlier project of the author's,
// with a second tier for the macOS bridge.
package deps

import (
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const module = "github.com/anthonyrego/reel"

// pure lists directories (relative to the repo root) whose packages are
// plain Go: no SDL, and no Objective-C either (neither purego nor
// internal/mac), so they run in tests without a GPU, a window or a device.
// A listed directory that doesn't exist yet is skipped; its subpackages are
// included.
var pure = []string{
	"internal/area",
	"internal/avsync",
	"internal/config",
	"internal/draw",
	"internal/geom",
	"internal/library",
	"internal/logx",
	"internal/mixer",
	"internal/mp4",
	"internal/session",
	"internal/settings",
	"internal/text",
	"internal/theme",
	"internal/thumb",
	"internal/timeline",
	"internal/ui",
}

// bridge lists the macOS bridge and the recording pipeline over it: they
// call Objective-C through purego but never import SDL, so recording runs
// without a window.
var bridge = []string{
	"internal/mac",
	"internal/record",
}

// TestPureDoesNotImportSDLOrObjC checks the pure tier.
func TestPureDoesNotImportSDLOrObjC(t *testing.T) {
	walk(t, pure, []string{"github.com/Zyko0/go-sdl3", "github.com/ebitengine/purego", module + "/internal/mac"})
}

// TestBridgeDoesNotImportSDL checks the bridge tier.
func TestBridgeDoesNotImportSDL(t *testing.T) {
	walk(t, bridge, []string{"github.com/Zyko0/go-sdl3"})
}

// walk follows imports in-process with go/build (so the test cache sees
// every file it reads) from every package under dirs, and fails if any
// chain of this module's packages reaches an import with a banned prefix.
func walk(t *testing.T, dirs, banned []string) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	var starts []string
	for _, dir := range dirs {
		filepath.WalkDir(filepath.Join(root, dir), func(p string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				if rel, _ := filepath.Rel(root, p); rel != "" {
					starts = append(starts, module+"/"+filepath.ToSlash(rel))
				}
			}
			return nil
		})
	}

	seen := map[string]bool{}
	var visit func(path string, chain []string)
	visit = func(path string, chain []string) {
		if seen[path] {
			return
		}
		seen[path] = true
		dir := filepath.Join(root, strings.TrimPrefix(path, module))
		pkg, err := build.ImportDir(dir, 0)
		if err != nil {
			return // no Go files (e.g. a fonts directory)
		}
		chain = append(chain, path)
		for _, imp := range pkg.Imports {
			for _, b := range banned {
				if strings.HasPrefix(imp, b) {
					t.Errorf("%s imports %s via %s", chain[0], imp, strings.Join(chain, " -> "))
				}
			}
			if strings.HasPrefix(imp, module+"/") {
				visit(imp, chain)
			}
		}
	}
	for _, s := range starts {
		visit(s, nil)
	}
	if len(seen) == 0 {
		t.Fatalf("walked no packages; is the repo root right (%s)?", root)
	}
}
