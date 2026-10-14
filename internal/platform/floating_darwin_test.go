package platform

import (
	"strings"
	"testing"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// TestFloatingSelectors checks every selector Float, WindowNumber,
// WindowFrameSize and WindowState send against AppKit and SDL: an unknown
// one raises an exception that kills the process.
//
// Copied from an earlier project of the author's.
func TestFloatingSelectors(t *testing.T) {
	LoadSDL()
	checkSelectors(t, floatingSelectors)
	if err := loadPointerProperty(); err != nil {
		t.Error(err)
	}
}

// TestAppearanceSelectors checks DarkWindow's selectors and constants.
func TestAppearanceSelectors(t *testing.T) {
	LoadSDL()
	checkSelectors(t, appearanceSelectors)
	for _, name := range appearanceSymbols {
		if _, err := purego.Dlsym(purego.RTLD_DEFAULT, name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// checkSelectors checks each class (a "+" prefix: its class methods)
// responds to its selectors.
func checkSelectors(t *testing.T, table map[string][]string) {
	t.Helper()
	for cls, names := range table {
		name, classMethod := strings.CutPrefix(cls, "+")
		c := objc.GetClass(name)
		if c == 0 {
			t.Errorf("no class %s", name)
			continue
		}
		check := objc.RegisterName("instancesRespondToSelector:")
		if classMethod {
			check = objc.RegisterName("respondsToSelector:")
		}
		for _, s := range names {
			if !objc.Send[bool](objc.ID(c), check, objc.RegisterName(s)) {
				t.Errorf("%s doesn't respond to %s", cls, s)
			}
		}
	}
}

func TestHotKeyFuncs(t *testing.T) {
	lib, err := purego.Dlopen(carbonPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range carbonFuncs {
		if _, err := purego.Dlsym(lib, name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
