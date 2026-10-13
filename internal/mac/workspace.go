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
