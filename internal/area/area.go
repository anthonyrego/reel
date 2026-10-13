// Package area works out the pixel size a recording is encoded at: the
// content's size in points times the display's scale, capped so H.264 can
// encode it, with even sides (4:2:0 video can't have odd ones). Pure.
package area

import "math"

// MaxW and MaxH bound the encoded frame: H.264 level 5.2 tops out at
// 4096×2304 (36864 macroblocks), and a HiDPI 4K panel's backing store
// (5120×2880, or 6016×3384 at "looks like 3008×1692") is beyond it.
// Recordings are scaled down to fit, keeping the aspect; a MacBook's own
// panel (3456×2234) fits as it is.
const (
	MaxW = 4096
	MaxH = 2304
)

// Pixels is the encoded size of content w×h points on a display with
// scale pixels per point, scaled down (never up) to fit maxW×maxH, with
// even sides of at least 2.
func Pixels(w, h, scale float64, maxW, maxH int) (int, int) {
	if scale <= 0 {
		scale = 1
	}
	pw, ph := w*scale, h*scale
	if pw <= 0 || ph <= 0 {
		return 2, 2
	}
	if k := math.Min(float64(maxW)/pw, float64(maxH)/ph); k < 1 {
		pw, ph = pw*k, ph*k
	}
	return even(pw), even(ph)
}

// even rounds down to an even number, at least 2.
func even(v float64) int {
	n := int(math.Floor(v+1e-6)) &^ 1 // 2303.9999… is 2304
	return max(n, 2)
}
