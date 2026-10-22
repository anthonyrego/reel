package mac

import (
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/ebitengine/purego"
)

// The main goroutine keeps the main thread, which runs its run loop while
// the tests run on others: AVPlayer says what it's doing there, as it does
// in the app (whose main thread SDL's event loop runs).
func init() { runtime.LockOSThread() }

func TestMain(m *testing.M) {
	cf, err := purego.Dlopen(coreFoundationPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		panic(err)
	}
	mode, err := purego.Dlsym(cf, "kCFRunLoopDefaultMode")
	if err != nil {
		panic(err)
	}
	var runInMode func(mode uintptr, seconds float64, returnAfterSource bool) int32
	purego.RegisterLibFunc(&runInMode, cf, "CFRunLoopRunInMode")
	code := make(chan int, 1)
	go func() { code <- m.Run() }()
	for {
		select {
		case c := <-code:
			os.Exit(c)
		default:
		}
		if runInMode(deref(mode), 0.02, false) == 1 { // kCFRunLoopRunFinished: nothing to run
			time.Sleep(20 * time.Millisecond)
		}
	}
}
