// Copied from an earlier project of the author's (⌃⌥V there, ⌃⌥R here).
package platform

import (
	"errors"
	"fmt"
	"sync"

	"github.com/ebitengine/purego"
)

// Carbon's hot keys: registered keys reach the app from any other app, with
// no Accessibility or Input Monitoring permission (a global NSEvent monitor
// would need one).
const (
	carbonPath = "/System/Library/Frameworks/Carbon.framework/Carbon"

	eventClassKeyboard   = 'k'<<24 | 'e'<<16 | 'y'<<8 | 'b'
	eventHotKeyPressed   = 5
	hotKeySignature      = 'r'<<24 | 'e'<<16 | 'e'<<8 | 'l'
	keyCodeR             = 0x0F // kVK_ANSI_R
	modControl           = 1 << 12
	modOption            = 1 << 11
	eventHotKeyExistsErr = -9878
)

// HotKeyName is the global key OnHotKey registers.
const HotKeyName = "⌃⌥R"

// carbonFuncs are the Carbon functions the hot key uses, by name (checked
// by the tests).
var carbonFuncs = []string{"GetApplicationEventTarget", "InstallEventHandler", "RegisterEventHotKey"}

type eventTypeSpec struct{ class, kind uint32 }

var hotKey struct {
	once sync.Once
	fn   func()
}

// OnHotKey calls fn each time ⌃⌥R is pressed, whichever app has focus.
// Carbon calls it on the main thread while SDL pumps events (in WaitEvent),
// so fn should only note the press and wake the loop. Call it once, on the
// main thread.
func OnHotKey(fn func()) error {
	var err error
	hotKey.once.Do(func() { err = registerHotKey(fn) })
	return err
}

func registerHotKey(fn func()) error {
	lib, err := purego.Dlopen(carbonPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return fmt.Errorf("hot key: %w", err)
	}
	for _, name := range carbonFuncs {
		if _, err := purego.Dlsym(lib, name); err != nil {
			return fmt.Errorf("hot key: %w", err)
		}
	}
	var (
		appTarget func() uintptr
		install   func(target, handler uintptr, n uint32, specs *eventTypeSpec, user uintptr, out *uintptr) int32
		// The EventHotKeyID struct ({signature, id uint32}) goes in one
		// register on arm64 and amd64, signature in the low half.
		register func(code, mods uint32, id uint64, target uintptr, opts uint32, out *uintptr) int32
	)
	purego.RegisterLibFunc(&appTarget, lib, "GetApplicationEventTarget")
	purego.RegisterLibFunc(&install, lib, "InstallEventHandler")
	purego.RegisterLibFunc(&register, lib, "RegisterEventHotKey")

	hotKey.fn = fn
	// Made once: purego never frees callbacks.
	cb := purego.NewCallback(func(next, event, user uintptr) uintptr {
		hotKey.fn()
		return 0 // noErr
	})
	target := appTarget()
	spec := eventTypeSpec{class: eventClassKeyboard, kind: eventHotKeyPressed}
	var handler uintptr
	if st := install(target, cb, 1, &spec, 0, &handler); st != 0 {
		return fmt.Errorf("hot key: InstallEventHandler: OSStatus %d", st)
	}
	var ref uintptr
	switch st := register(keyCodeR, modControl|modOption, uint64(hotKeySignature)|1<<32, target, 0, &ref); st {
	case 0:
		return nil
	case eventHotKeyExistsErr:
		return errors.New("hot key: " + HotKeyName + " is taken by another app")
	default:
		return fmt.Errorf("hot key: RegisterEventHotKey: OSStatus %d", st)
	}
}
