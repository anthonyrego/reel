package mac

import (
	"fmt"
	"sync"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

const appKitPath = "/System/Library/Frameworks/AppKit.framework/AppKit"

var sleepWatch struct {
	once sync.Once
	err  error
	fns  []func(sleeping bool)
	mu   sync.Mutex
}

// OnSleep calls fn when the Mac is about to sleep (true) and when it has
// woken (false), from NSWorkspace's notifications: a recording pauses
// across a sleep rather than holding it as a still stretch. They're
// delivered on the main thread's run loop, so only an app gets them.
func OnSleep(fn func(sleeping bool)) error {
	if err := load(); err != nil {
		return err
	}
	sleepWatch.mu.Lock()
	sleepWatch.fns = append(sleepWatch.fns, fn)
	sleepWatch.mu.Unlock()
	sleepWatch.once.Do(func() { sleepWatch.err = watchSleep() })
	return sleepWatch.err
}

func watchSleep() error {
	appKit, err := purego.Dlopen(appKitPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return err
	}
	names := map[string]bool{"NSWorkspaceWillSleepNotification": true, "NSWorkspaceDidWakeNotification": false}
	withPool(func() {
		center := class("NSWorkspace").Send(sel("sharedWorkspace")).Send(sel("notificationCenter"))
		for name, sleeping := range names {
			p, e := purego.Dlsym(appKit, name)
			if e != nil {
				err = e
				return
			}
			note := objc.ID(deref(p))
			if note == 0 {
				err = fmt.Errorf("mac: %s is nil", name)
				return
			}
			// The block lives as long as the process: never released.
			block := objc.NewBlock(func(_ objc.Block, _ objc.ID) {
				sleepWatch.mu.Lock()
				fns := append([]func(bool){}, sleepWatch.fns...)
				sleepWatch.mu.Unlock()
				for _, fn := range fns {
					fn(sleeping)
				}
			})
			center.Send(sel("addObserverForName:object:queue:usingBlock:"), note, objc.ID(0), objc.ID(0), block).Send(sel("retain"))
		}
	})
	return err
}

var activeWatch struct {
	once sync.Once
	fns  []func(active bool)
	mu   sync.Mutex
}

// OnActive calls fn when reel becomes the active app (true: the user
// switched to it) and when another app does (false), from NSApplication's
// notifications on the main thread's run loop, so only an app gets them.
// AppActive is the state now.
func OnActive(fn func(active bool)) error {
	if err := load(); err != nil {
		return err
	}
	activeWatch.mu.Lock()
	activeWatch.fns = append(activeWatch.fns, fn)
	activeWatch.mu.Unlock()
	activeWatch.once.Do(func() {
		withPool(func() {
			center := class("NSNotificationCenter").Send(sel("defaultCenter"))
			for name, active := range map[string]bool{"NSApplicationDidBecomeActiveNotification": true, "NSApplicationDidResignActiveNotification": false} {
				// The block lives as long as the process: never released.
				block := objc.NewBlock(func(_ objc.Block, _ objc.ID) {
					activeWatch.mu.Lock()
					fns := append([]func(bool){}, activeWatch.fns...)
					activeWatch.mu.Unlock()
					for _, fn := range fns {
						fn(active)
					}
				})
				center.Send(sel("addObserverForName:object:queue:usingBlock:"), constant(name), objc.ID(0), objc.ID(0), block).Send(sel("retain"))
			}
		})
	})
	return nil
}

// AppActive reports whether reel is the active app (the one in front).
func AppActive() bool {
	if load() != nil {
		return false
	}
	var active bool
	withPool(func() {
		active = objc.Send[bool](class("NSRunningApplication").Send(sel("currentApplication")), sel("isActive"))
	})
	return active
}

// nsEventModifierFlagOption is NSEventModifierFlagOption, an enum value.
const nsEventModifierFlagOption = 1 << 19

// OptionHeld reports whether ⌥ is held now, whichever app is in front
// (SDL's modifier state only follows the keys while reel is).
func OptionHeld() bool {
	if load() != nil {
		return false
	}
	return objc.Send[uint](class("NSEvent"), sel("modifierFlags"))&nsEventModifierFlagOption != 0
}
