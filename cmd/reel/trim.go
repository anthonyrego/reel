package main

// trim: copies part of a recording to a new MP4 without re-encoding, the
// way the app's Trim does. It records nothing, so agents run it.

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/anthonyrego/reel/internal/library"
	"github.com/anthonyrego/reel/internal/mac"
	"github.com/anthonyrego/reel/internal/mp4"
)

func init() {
	register(&command{name: "trim", summary: "copy part of a recording to a new MP4 without re-encoding (agents: safe)", run: runTrim})
}

func runTrim(args []string) error {
	fs := flag.NewFlagSet("trim", flag.ContinueOnError)
	from := fs.Duration("from", 0, "where the copy starts")
	to := fs.Duration("to", 0, "where it ends (0: the end)")
	out := fs.String("out", "", `the MP4 to write (default: beside FILE, "… trimmed.mp4")`)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: reel trim -from 2s -to 5s [-out PATH] FILE.mp4")
	}
	src := fs.Arg(0)
	f, err := mp4.ReadFile(src)
	if err != nil {
		return err
	}
	if *to == 0 {
		*to = seconds(f.Duration)
	}
	dst := *out
	if dst == "" {
		dst = library.TrimmedPath(src)
	}
	start := time.Now()
	if err := trimTo(src, dst, *from, *to); err != nil {
		return err
	}
	g, err := mp4.ReadFile(dst)
	if err != nil {
		return err
	}
	fmt.Printf("wrote %s in %v: %.3f s, %.3f to %.3f s of %.3f s\n", dst, time.Since(start).Round(time.Millisecond),
		g.Duration, from.Seconds(), to.Seconds(), f.Duration)
	for _, t := range g.Tracks {
		fmt.Printf("      %s: %.3f s stored, playing from %.3f s in (a player that ignores edit lists starts there)\n",
			t.Handler, t.Duration, t.Lead)
	}
	return nil
}

// trimTo copies from..to of the recording at src to dst without
// re-encoding. It writes a hidden file beside dst first (the recordings
// list skips it) and links it into place only when it's whole, moov
// first; it never writes over a file.
func trimTo(src, dst string, from, to time.Duration) error {
	tmp, err := trimHidden(src, filepath.Dir(dst), from, to)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	return os.Link(tmp, dst)
}

// trimReplace trims the recording at src in place: the trimmed copy takes
// its name and the original goes to the Trash. If the Trash won't take it,
// both are kept, the trim as a copy, and the error says so. It returns
// where the trim is.
func trimReplace(src string, from, to time.Duration) (string, error) {
	tmp, err := trimHidden(src, filepath.Dir(src), from, to)
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp)
	if err := mac.Trash(src); err != nil {
		dst := library.TrimmedPath(src)
		if err2 := os.Link(tmp, dst); err2 != nil {
			return "", err2
		}
		return dst, fmt.Errorf("saved as a copy: %w", err)
	}
	return src, os.Link(tmp, src)
}

// trimHidden trims src into a new hidden file in dir.
func trimHidden(src, dir string, from, to time.Duration) (string, error) {
	tmp := filepath.Join(dir, fmt.Sprintf(".trim-%d-%s", time.Now().UnixNano(), filepath.Base(src)))
	if err := mac.Trim(src, tmp, from, to); err != nil {
		return "", err
	}
	// The export puts moov first; make sure, since the list shows only
	// files that do. Then start the edit list on a frame, so players built
	// on ffmpeg don't play the picture early.
	if err := mp4.FastStart(tmp); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if _, err := mp4.AlignEdits(tmp); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

// seconds is s seconds as a Duration.
func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
