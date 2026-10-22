package mac

import (
	"testing"
	"time"
)

// openClip opens the test clip muted, frames w×h, and waits until it's
// ready.
func openClip(t *testing.T, w, h int) *Player {
	t.Helper()
	p, err := OpenPlayer(clip)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	p.SetMuted(true)
	p.SetSize(w, h)
	for deadline := time.Now().Add(5 * time.Second); ; {
		ok, err := p.Ready()
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			return p
		}
		if time.Now().After(deadline) {
			t.Fatal("not ready after 5 s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// frameAt waits for a new frame at the host time now.
func frameAt(t *testing.T, p *Player) (at time.Duration, w, h int, px [4]byte) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		at, ok := p.Frame(HostTime().Seconds(), func(pix []byte, fw, fh, stride int) {
			w, h = fw, fh
			i := fh/2*stride + fw/2*4
			copy(px[:], pix[i:i+4])
		})
		if ok {
			return at, w, h, px
		}
	}
	t.Fatal("no frame after 3 s")
	return
}

// TestPlayerSeek: a seek shows the frame there, at the size asked for.
func TestPlayerSeek(t *testing.T) {
	p := openClip(t, 160, 90)
	if d := p.Duration(); d < 3990*time.Millisecond || d > 4010*time.Millisecond {
		t.Errorf("duration %v, want 4 s", d)
	}
	p.Seek(2*time.Second, true)
	at, w, h, _ := frameAt(t, p)
	if w != 160 || h != 90 {
		t.Errorf("frame %d×%d, want 160×90", w, h)
	}
	if at < 1990*time.Millisecond || at > 2010*time.Millisecond {
		t.Errorf("frame at %v, want 2 s", at)
	}
	if got := p.Time(); got < 1990*time.Millisecond || got > 2010*time.Millisecond {
		t.Errorf("time %v after the seek, want 2 s", got)
	}
	if p.Playing() {
		t.Error("playing after a seek, never told to")
	}
}

// TestPlayerPlays: playing moves the clock and the frames along with it,
// and it stops at the end.
func TestPlayerPlays(t *testing.T) {
	p := openClip(t, 320, 180)
	p.Seek(3*time.Second, true)
	frameAt(t, p)
	p.Play()
	if !p.Playing() {
		t.Fatal("not playing after Play")
	}
	start := time.Now()
	var last time.Duration
	frames := 0
	for time.Since(start) < 1500*time.Millisecond {
		if at, ok := p.Frame(HostTime().Seconds(), func([]byte, int, int, int) {}); ok {
			if at < last {
				t.Errorf("frame at %v after one at %v", at, last)
			}
			last = at
			frames++
		}
		time.Sleep(4 * time.Millisecond)
	}
	if p.Playing() {
		t.Error("still playing after the end")
	}
	if got := p.Time(); got < 3950*time.Millisecond {
		t.Errorf("stopped at %v, want the end (4 s)", got)
	}
	// 1 s of a 30 fps clip.
	if frames < 25 || frames > 32 {
		t.Errorf("%d frames in the last second, want about 30", frames)
	}
	t.Logf("%d frames, the last at %v", frames, last)
}
