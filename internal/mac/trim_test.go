package mac

import (
	"bytes"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/anthonyrego/reel/internal/mp4"
)

// clip is 4 s of ffmpeg's testsrc2 (a clock and moving shapes) at 320×180,
// 30 fps, a keyframe every second, with a 440 Hz tone, made with:
//
//	ffmpeg -f lavfi -i testsrc2=size=320x180:rate=30:duration=4 \
//	  -f lavfi -i sine=frequency=440:sample_rate=48000:duration=4 \
//	  -c:v libx264 -profile:v high -pix_fmt yuv420p -crf 32 -g 30 -keyint_min 30 \
//	  -sc_threshold 0 -bf 0 -c:a aac -b:a 64k -ac 2 -movflags +faststart \
//	  -map_metadata -1 -fflags +bitexact -flags:v +bitexact -flags:a +bitexact clip.mp4
const clip = "testdata/clip.mp4"

func TestTrim(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "trimmed.mp4")
	if err := Trim(clip, dst, 1500*time.Millisecond, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	f, err := mp4.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !f.FastStart() {
		t.Errorf("not fast start: %v", f.Top)
	}
	if math.Abs(f.Duration-1.5) > 0.04 {
		t.Errorf("duration %.3f s, want 1.5", f.Duration)
	}
	v, ok := f.Track("vide")
	if !ok || v.Width != 320 || v.Height != 180 {
		t.Fatalf("video %+v", v)
	}
	// The copy starts at the keyframe at 1 s; the edit list skips to 1.5.
	if math.Abs(v.Lead-0.5) > 1.0/30 {
		t.Errorf("video lead %.3f s, want 0.5", v.Lead)
	}
	if _, ok := f.Track("soun"); !ok {
		t.Error("no audio track")
	}
	t.Logf("%+v", f)

	if err := Trim(clip, dst, 0, time.Second); !errors.Is(err, ErrExists) {
		t.Errorf("over an existing file: %v", err)
	}
	if err := Trim(clip, filepath.Join(t.TempDir(), "x.mp4"), 2*time.Second, time.Second); err == nil {
		t.Error("a backwards range")
	}
}

func TestFrames(t *testing.T) {
	f, err := OpenFrames(clip)
	if err != nil {
		t.Fatal(err)
	}
	var imgs [][]byte
	for _, at := range []time.Duration{time.Second, 2 * time.Second, 2*time.Second + 500*time.Millisecond} {
		img, got, err := f.At(at, 160)
		if err != nil {
			t.Fatalf("at %v: %v", at, err)
		}
		if b := img.Bounds(); b.Dx() != 160 || b.Dy() != 90 {
			t.Errorf("at %v: %v, want 160×90", at, b)
		}
		// The time comes through the handler's CMTime pointer.
		if d := got - at; d < -time.Second/30 || d > time.Second/30 {
			t.Errorf("asked for %v, got the frame at %v", at, got)
		}
		imgs = append(imgs, img.Pix)
	}
	if bytes.Equal(imgs[0], imgs[1]) || bytes.Equal(imgs[1], imgs[2]) {
		t.Error("different times gave the same picture")
	}
	if img, _, err := f.At(time.Second, 0); err != nil || img.Bounds().Dx() != 320 {
		t.Errorf("full size: %v %v", img.Bounds(), err)
	}
	f.Close()
	f.Close()
	if _, _, err := f.At(time.Second, 160); err == nil {
		t.Error("a frame after Close")
	}
}
