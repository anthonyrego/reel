package mac

// Trimming: part of a recording copied to a new MP4 without re-encoding,
// through AVAssetExportSession's passthrough preset. A copy can only
// start at a keyframe, so it starts at the one before the cut and says
// in an edit list where playing starts: a player that reads edit lists
// (QuickTime, browsers, ffmpeg) starts at the cut.
//
// AVAssetExportSession raises exceptions for an output type it doesn't
// support and for an export started without one, so Trim checks the type
// against supportedFileTypes and sets it before anything starts.

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/ebitengine/purego/objc"
)

// AVAssetExportSessionStatus values (AVAssetExportSession.h).
const (
	avExportCompleted = 3
	avExportCancelled = 5
)

// exportTimeout bounds an export. Passthrough copies samples, so even a
// long recording takes seconds; this is for one that's stuck.
const exportTimeout = 5 * time.Minute

// Trim copies from..to of the MP4 at src to a new MP4 at dst, moov first,
// without re-encoding. dst must not exist; on an error nothing is left
// there. Any goroutine; it waits for the copy.
func Trim(src, dst string, from, to time.Duration) error {
	if err := load(); err != nil {
		return err
	}
	if from < 0 || to <= from {
		return fmt.Errorf("trim: %v to %v isn't a range", from, to)
	}
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("trim: %w", err)
	}
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("%s: %w", dst, ErrExists)
	}
	var err error
	withPool(func() { err = export(src, dst, from, to) })
	if err != nil {
		os.Remove(dst)
	}
	return err
}

func export(src, dst string, from, to time.Duration) error {
	asset := class("AVURLAsset").Send(sel("URLAssetWithURL:options:"), fileURL(src), objc.ID(0))
	if asset == 0 {
		return fmt.Errorf("trim: can't open %s", src)
	}
	s := class("AVAssetExportSession").Send(sel("exportSessionWithAsset:presetName:"), asset, constant("AVAssetExportPresetPassthrough"))
	if s == 0 {
		return errors.New("trim: this file can't be copied without re-encoding")
	}
	mp4, ok := goString(constant("AVFileTypeMPEG4")), false
	each(s.Send(sel("supportedFileTypes")), func(t objc.ID) { ok = ok || goString(t) == mp4 })
	if !ok {
		return errors.New("trim: this file can't be copied to an MP4")
	}
	s.Send(sel("setOutputURL:"), fileURL(dst))
	s.Send(sel("setOutputFileType:"), constant("AVFileTypeMPEG4"))
	s.Send(sel("setShouldOptimizeForNetworkUse:"), true)
	s.Send(sel("setTimeRange:"), CMTimeRange{Start: Nanos(int64(from)), Duration: Nanos(int64(to - from))})

	done := make(chan struct{})
	block := objc.NewBlock(func(objc.Block) { close(done) })
	defer block.Release()
	s.Send(sel("exportAsynchronouslyWithCompletionHandler:"), block)
	select {
	case <-done:
	case <-time.After(exportTimeout):
		s.Send(sel("cancelExport"))
		select { // let it stop writing before the file is removed
		case <-done:
		case <-time.After(callTimeout):
		}
		return fmt.Errorf("trim: %w", errTimeout)
	}
	switch objc.Send[int](s, sel("status")) {
	case avExportCompleted:
		return nil
	case avExportCancelled:
		return errors.New("trim: cancelled")
	default:
		return nsError("trim", s.Send(sel("error")))
	}
}
