package mac

// A still of a display, for picking an area on it.
//
// Copied from an earlier project of the author's (CaptureScreen, copyImage, toRGBA), by display ID.

import (
	"errors"
	"fmt"
	"image"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

var shot struct {
	imgW, imgH, imgStride     func(img uintptr) uint
	imgBits                   func(img uintptr) uint
	imgInfo                   func(img uintptr) uint32
	imgProvider, providerCopy func(uintptr) uintptr
	dataPtr                   func(data uintptr) unsafe.Pointer
	dataLen                   func(data uintptr) int
}

func loadScreenshot() error {
	cg, err := purego.Dlopen(coreGraphicsPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return err
	}
	cf, err := purego.Dlopen(coreFoundationPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return err
	}
	purego.RegisterLibFunc(&shot.imgW, cg, "CGImageGetWidth")
	purego.RegisterLibFunc(&shot.imgH, cg, "CGImageGetHeight")
	purego.RegisterLibFunc(&shot.imgStride, cg, "CGImageGetBytesPerRow")
	purego.RegisterLibFunc(&shot.imgBits, cg, "CGImageGetBitsPerPixel")
	purego.RegisterLibFunc(&shot.imgInfo, cg, "CGImageGetBitmapInfo")
	purego.RegisterLibFunc(&shot.imgProvider, cg, "CGImageGetDataProvider")
	purego.RegisterLibFunc(&shot.providerCopy, cg, "CGDataProviderCopyData")
	purego.RegisterLibFunc(&shot.dataPtr, cf, "CFDataGetBytePtr")
	purego.RegisterLibFunc(&shot.dataLen, cf, "CFDataGetLength")
	return nil
}

// Screenshot takes display id as it is now, without reel's windows, its
// long edge at most longEdge pixels (0: the display's own). It needs
// Screen Recording. Any goroutine; it takes tens of milliseconds.
func Screenshot(id uint32, longEdge int) (*image.RGBA, error) {
	if err := load(); err != nil {
		return nil, err
	}
	if !lib.preflight() {
		return nil, ErrNoScreenAccess
	}
	var out *image.RGBA
	var err error
	withPool(func() {
		var content objc.ID
		if content, err = shareableContent(); err != nil {
			return
		}
		defer content.Send(sel("release"))
		var display objc.ID
		each(content.Send(sel("displays")), func(d objc.ID) {
			if objc.Send[uint32](d, sel("displayID")) == id {
				display = d
			}
		})
		if display == 0 {
			err = fmt.Errorf("screenshot: no display %d", id)
			return
		}
		var filter objc.ID
		if filter, err = contentFilter(content, Target{DisplayID: id}); err != nil {
			return
		}
		frame := objc.Send[cgRect](display, sel("frame"))
		scale := float64(objc.Send[float32](filter, sel("pointPixelScale")))
		if scale <= 0 {
			scale = 1
		}
		w, h := frame.W*scale, frame.H*scale
		if long := max(w, h); longEdge > 0 && long > float64(longEdge) {
			w, h = w*float64(longEdge)/long, h*float64(longEdge)/long
		}
		config := class("SCStreamConfiguration").Send(sel("alloc")).Send(sel("init")).Send(sel("autorelease"))
		config.Send(sel("setWidth:"), uint(w+0.5))
		config.Send(sel("setHeight:"), uint(h+0.5))
		config.Send(sel("setShowsCursor:"), false)

		type result struct {
			img *image.RGBA
			err error
		}
		got := make(chan result, 1)
		done := objc.NewBlock(func(_ objc.Block, img uintptr, nserr objc.ID) {
			if img == 0 {
				got <- result{err: nsError("screenshot", nserr)}
				return
			}
			rgba, err := copyImage(img)
			got <- result{rgba, err}
		})
		defer done.Release()
		class("SCScreenshotManager").Send(sel("captureImageWithFilter:configuration:completionHandler:"), filter, config, done)
		select {
		case r := <-got:
			out, err = r.img, r.err
		case <-time.After(callTimeout):
			err = fmt.Errorf("screenshot: %w", errTimeout)
		}
	})
	return out, err
}

// The bitmap info bits copyImage reads.
const (
	cgAlphaMask        = 0x1f
	cgAlphaFirst       = 2 // premultiplied first
	cgAlphaNoneSkipFst = 6
	cgByteOrderMask    = 0x7000
	cgByteOrder32Lit   = 2 << 12
)

// copyImage copies a 32-bit CGImage into an RGBA image. ScreenCaptureKit
// gives BGRA (32-bit little-endian, alpha first).
func copyImage(img uintptr) (*image.RGBA, error) {
	w, h, stride := int(shot.imgW(img)), int(shot.imgH(img)), int(shot.imgStride(img))
	if bits := shot.imgBits(img); bits != 32 {
		return nil, fmt.Errorf("screenshot: %d bits a pixel", bits)
	}
	data := shot.providerCopy(shot.imgProvider(img))
	if data == 0 {
		return nil, errors.New("screenshot: no pixels")
	}
	defer lib.cfRelease(data)
	n := shot.dataLen(data)
	if n < stride*h {
		return nil, errors.New("screenshot: short image")
	}
	src := unsafe.Slice((*byte)(shot.dataPtr(data)), n)
	info := shot.imgInfo(img)
	alpha := info & cgAlphaMask
	bgra := info&cgByteOrderMask == cgByteOrder32Lit && (alpha == cgAlphaFirst || alpha == cgAlphaNoneSkipFst)
	return toRGBA(src, w, h, stride, bgra), nil
}

// toRGBA copies 32-bit pixels, rows stride bytes apart, into an opaque
// RGBA image, swapping blue and red when bgra.
func toRGBA(src []byte, w, h, stride int, bgra bool) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		row := src[y*stride : y*stride+w*4]
		dst := out.Pix[y*out.Stride : y*out.Stride+w*4]
		if !bgra {
			copy(dst, row)
			continue
		}
		for x := 0; x < w*4; x += 4 {
			dst[x], dst[x+1], dst[x+2], dst[x+3] = row[x+2], row[x+1], row[x], 0xff
		}
	}
	return out
}
