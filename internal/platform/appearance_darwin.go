package platform

// The main window's look: reel is always dark, so the app takes the dark
// appearance whatever the system's (the title bar, the traffic lights),
// and the title bar is see-through over the window's own color, so it
// reads as part of the content.

import (
	"errors"
	"sync"
	"unsafe"

	"github.com/Zyko0/go-sdl3/sdl"
	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// appearanceSelectors are every selector DarkWindow sends, by class
// (checked against AppKit by the tests).
var appearanceSelectors = map[string][]string{
	"NSWindow":       {"setAppearance:", "setTitlebarAppearsTransparent:", "setTitleVisibility:", "setBackgroundColor:"},
	"NSApplication":  {"setAppearance:"},
	"+NSApplication": {"sharedApplication"},
	"+NSAppearance":  {"appearanceNamed:"},
	"+NSColor":       {"colorWithSRGBRed:green:blue:alpha:"},
}

// appearanceSymbols are the AppKit constants DarkWindow reads.
var appearanceSymbols = []string{"NSAppearanceNameDarkAqua"}

// nsWindowTitleHidden is NSWindowTitleVisibility's hidden.
const nsWindowTitleHidden = 1

var darkAqua struct {
	once sync.Once
	id   objc.ID
	err  error
}

// DarkWindow gives the app the dark appearance and makes w's title bar
// see-through over bg (0xRRGGBB), with no title. Main thread.
func DarkWindow(w *sdl.Window, bg uint32) error {
	win, err := nsWindow(w)
	if err != nil {
		return err
	}
	darkAqua.once.Do(func() {
		sym, err := purego.Dlsym(purego.RTLD_DEFAULT, appearanceSymbols[0])
		if err != nil {
			darkAqua.err = err
			return
		}
		// The extern's value, read through &sym so vet's unsafe.Pointer
		// check holds (as internal/mac's deref).
		name := **(**objc.ID)(unsafe.Pointer(&sym))
		darkAqua.id = objc.ID(objc.GetClass("NSAppearance")).Send(sel("appearanceNamed:"), name)
		if darkAqua.id == 0 {
			darkAqua.err = errors.New("no dark appearance")
		}
	})
	if darkAqua.err != nil {
		return darkAqua.err
	}
	app := objc.ID(objc.GetClass("NSApplication")).Send(sel("sharedApplication"))
	app.Send(sel("setAppearance:"), darkAqua.id)
	win.Send(sel("setAppearance:"), darkAqua.id)
	color := objc.ID(objc.GetClass("NSColor")).Send(sel("colorWithSRGBRed:green:blue:alpha:"),
		float64(bg>>16&0xff)/255, float64(bg>>8&0xff)/255, float64(bg&0xff)/255, 1.0)
	win.Send(sel("setBackgroundColor:"), color)
	win.Send(sel("setTitlebarAppearsTransparent:"), true)
	win.Send(sel("setTitleVisibility:"), nsWindowTitleHidden)
	return nil
}
