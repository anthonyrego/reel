package area

import "testing"

func TestPixels(t *testing.T) {
	for _, c := range []struct {
		name         string
		w, h, scale  float64
		wantW, wantH int
	}{
		{"MacBook Pro 16 at 2x fits as it is", 1728, 1117, 2, 3456, 2234},
		{"a 4K panel scaled to 3008 points is capped", 3008, 1692, 2, 4096, 2304},
		{"5K at 2560 points is capped", 2560, 1440, 2, 4096, 2304},
		{"1x 1080p is as it is", 1920, 1080, 1, 1920, 1080},
		{"odd window sides round down to even", 801, 601, 1, 800, 600},
		{"a tall window is capped by height", 1000, 3000, 2, 768, 2304},
		{"nothing is still 2×2", 0, 0, 2, 2, 2},
		{"no scale means 1", 640, 480, 0, 640, 480},
	} {
		w, h := Pixels(c.w, c.h, c.scale, MaxW, MaxH)
		if w != c.wantW || h != c.wantH {
			t.Errorf("%s: %v×%v@%v = %d×%d, want %d×%d", c.name, c.w, c.h, c.scale, w, h, c.wantW, c.wantH)
		}
		if w%2 != 0 || h%2 != 0 || w > MaxW || h > MaxH {
			t.Errorf("%s: %d×%d isn't even or within the cap", c.name, w, h)
		}
	}
}

func TestVideoBitrate(t *testing.T) {
	if b := VideoBitrate(4096, 2304, 30); b != MaxVideoBitrate {
		t.Errorf("4K: %d", b)
	}
	if b := VideoBitrate(960, 600, 30); b != 1_382_400 {
		t.Errorf("small window: %d", b)
	}
	if b := VideoBitrate(320, 200, 30); b != MinVideoBitrate {
		t.Errorf("tiny: %d", b)
	}
	if mb := float64(MaxVideoBitrate+AudioBitrate) * 1800 / 8 / 1e6; mb >= 1000 {
		t.Errorf("30 minutes is %.0f MB", mb)
	}
}
