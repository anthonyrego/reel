package mac

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// TestBridge checks every selector, function, constant and protocol the
// package uses against the installed frameworks, without touching a device
// or asking for a permission: an unknown selector raises an exception that
// kills the process, so it's caught here instead.
func TestBridge(t *testing.T) {
	if err := load(); err != nil {
		t.Fatal(err)
	}
	responds := sel("respondsToSelector:")
	instancesRespond := sel("instancesRespondToSelector:")
	for cls, names := range selectors {
		name, classMethod := strings.CutPrefix(cls, "+")
		c := objc.GetClass(name)
		if c == 0 {
			t.Errorf("no class %s", name)
			continue
		}
		for _, s := range names {
			check := instancesRespond
			if classMethod {
				check = responds
			}
			if !objc.Send[bool](objc.ID(c), check, sel(s)) {
				t.Errorf("%s doesn't respond to %s", cls, s)
			}
		}
	}
	for path, names := range funcs {
		h, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range names {
			if _, err := purego.Dlsym(h, name); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
	}
	for _, names := range constants {
		for _, name := range names {
			if constant(name) == 0 {
				t.Errorf("constant %s is nil", name)
			}
		}
	}
	if err := loadOutput(); err != nil {
		t.Fatalf("output class: %v", err)
	}
	for _, name := range protocols {
		p := objc.GetProtocol(name)
		if p == nil || !objc.Send[bool](objc.ID(output.class), sel("conformsToProtocol:"), p) {
			t.Errorf("the output class doesn't conform to %s", name)
		}
	}
	for _, s := range []string{"stream:didOutputSampleBuffer:ofType:", "stream:didStopWithError:"} {
		if !objc.Send[bool](objc.ID(output.class), sel("instancesRespondToSelector:"), sel(s)) {
			t.Errorf("the output class doesn't respond to %s", s)
		}
	}
}

// TestStrings round-trips a string and an array through Foundation.
func TestStrings(t *testing.T) {
	if err := load(); err != nil {
		t.Fatal(err)
	}
	withPool(func() {
		const s = "Réel — 録画"
		if got := goString(nsString(s)); got != s {
			t.Errorf("round trip = %q", got)
		}
		var got []string
		each(nsArray(nsString("a"), nsString("b")), func(id objc.ID) { got = append(got, goString(id)) })
		if strings.Join(got, ",") != "a,b" {
			t.Errorf("array = %v", got)
		}
		if n := objc.Send[uint](nsArray(), sel("count")); n != 0 {
			t.Errorf("empty array has %d", n)
		}
	})
}

func TestTransport(t *testing.T) {
	for tr, want := range map[Transport]string{
		TransportBuiltIn: "built-in", TransportBluetooth: "bluetooth", TransportBLE: "bluetooth",
		TransportUSB: "usb", 0: "unknown", Transport('t'<<24 | 'h'<<16 | 'u'<<8 | 'n'): "thun",
	} {
		if got := tr.String(); got != want {
			t.Errorf("%#x = %q, want %q", uint32(tr), got, want)
		}
	}
	if !TransportBLE.Bluetooth() || TransportUSB.Bluetooth() {
		t.Error("Bluetooth()")
	}
}

func TestCMTimeNanos(t *testing.T) {
	for _, c := range []struct {
		t    CMTime
		want int64
	}{
		{Nanos(123_456_789_012), 123_456_789_012},
		{TimeOf(3, 2), 1_500_000_000},
		{TimeOf(1, 3), 333_333_333},
		{TimeOf(1_000_000_000_000, 48000), 20_833_333_333_333_333},
		{CMTime{}, 0},
	} {
		if got := c.t.Nanos(); got != c.want {
			t.Errorf("%+v = %d, want %d", c.t, got, c.want)
		}
	}
}

// TestConstantsListed: every constant("…") the package reads is in the
// constants table (constant panics on one that isn't, which only shows
// when that path runs).
func TestConstantsListed(t *testing.T) {
	listed := map[string]bool{}
	for _, names := range constants {
		for _, n := range names {
			listed[n] = true
		}
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	use := regexp.MustCompile(`constant\("([A-Za-z0-9_]+)"\)`)
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range use.FindAllStringSubmatch(string(src), -1) {
			if !listed[m[1]] {
				t.Errorf("%s reads constant %s, which isn't in the constants table", f, m[1])
			}
		}
	}
}
