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
	if err := loadCamera(); err != nil {
		t.Fatalf("camera class: %v", err)
	}
	for cls, names := range protocols {
		c := objc.GetClass(cls)
		if c == 0 {
			t.Errorf("no class %s", cls)
			continue
		}
		for _, name := range names {
			p := objc.GetProtocol(name)
			if p == nil || !objc.Send[bool](objc.ID(c), sel("conformsToProtocol:"), p) {
				t.Errorf("%s doesn't conform to %s", cls, name)
			}
		}
	}
	for cls, sels := range map[string][]string{
		"ReelStreamOutput": {"stream:didOutputSampleBuffer:ofType:", "stream:didStopWithError:"},
		"ReelCameraOutput": {"captureOutput:didOutputSampleBuffer:fromConnection:"},
	} {
		for _, s := range sels {
			if !objc.Send[bool](objc.ID(objc.GetClass(cls)), sel("instancesRespondToSelector:"), sel(s)) {
				t.Errorf("%s doesn't respond to %s", cls, s)
			}
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

// TestOnSleepRegisters: the observers register without an exception (they
// can't be fired from a test).
func TestOnSleepRegisters(t *testing.T) {
	if err := OnSleep(func(bool) {}); err != nil {
		t.Fatal(err)
	}
}

// TestOnActive: the observers register without an exception and hear
// the notifications (posted here: a test binary can't become the active
// app).
func TestOnActive(t *testing.T) {
	var heard []bool
	if err := OnActive(func(active bool) { heard = append(heard, active) }); err != nil {
		t.Fatal(err)
	}
	_ = AppActive()
	withPool(func() {
		center := class("NSNotificationCenter").Send(sel("defaultCenter"))
		for _, name := range []string{"NSApplicationDidBecomeActiveNotification", "NSApplicationDidResignActiveNotification"} {
			center.Send(sel("postNotificationName:object:"), constant(name), objc.ID(0))
		}
	})
	if len(heard) != 2 || !heard[0] || heard[1] {
		t.Fatalf("heard %v, want [true false]", heard)
	}
}

// TestNotBundle: a test binary isn't Reel.app, so it lists no iPhones and
// can't ask for the camera (nor open one).
func TestNotBundle(t *testing.T) {
	if InBundle() {
		t.Fatal("a test binary reads as Reel.app")
	}
	if err := RequestCameraAccess(func(bool) { t.Error("asked") }); err != ErrNotBundle {
		t.Fatalf("RequestCameraAccess = %v, want ErrNotBundle", err)
	}
	if _, err := Cameras(); err != nil {
		t.Fatal(err)
	}
	if _, err := StartCamera("any", func(Sample) { t.Error("a frame") }, nil); err != ErrNotBundle {
		t.Fatalf("StartCamera = %v, want ErrNotBundle", err)
	}
}
