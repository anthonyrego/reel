package mac

import (
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
	for _, name := range protocols {
		if objc.GetProtocol(name) == nil {
			t.Errorf("no protocol %s", name)
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
