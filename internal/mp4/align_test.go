package mp4

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// stts is an stts box of (count, duration) runs.
func stts(runs ...[2]uint32) []byte {
	b := append(u32(0), u32(uint32(len(runs)))...)
	for _, r := range runs {
		b = append(b, u32(r[0])...)
		b = append(b, u32(r[1])...)
	}
	return box("stts", b)
}

// trimmed is ftyp, moov, mdat as a trim copied without re-encoding is: a
// video track (timescale 600) whose one edit starts at media time start,
// with samples of the durations runs gives and its chunk in stco, and an
// audio track whose edit skips its priming, with its chunk in co64. extra
// boxes go in the video's stbl.
func trimmed(start int32, runs [][2]uint32, extra ...[]byte) []byte {
	var n, dur uint32
	for _, r := range runs {
		n += r[0]
		dur += r[0] * r[1]
	}
	ftyp := box("ftyp", []byte("mp42"))
	mdat := box("mdat", []byte("AAAAxxBBBB"))
	moov := func(a, b uint64) []byte {
		stbl := append([][]byte{stts(runs...), table("stco", a)}, extra...)
		return box("moov", durationBox("mvhd", 600, 1800),
			box("trak", box("edts", elst(0, [2]int64{1800, int64(start)})),
				box("mdia", durationBox("mdhd", 600, dur), hdlr("vide"), box("minf", box("stbl", stbl...)))),
			box("trak", box("edts", elst(0, [2]int64{1800, 1024})),
				box("mdia", durationBox("mdhd", 48000, 145000), hdlr("soun"),
					box("minf", box("stbl", stts([2]uint32{141, 1024}), table("co64", b))))),
		)
	}
	size := uint64(len(ftyp) + len(moov(0, 0)))
	return bytes.Join([][]byte{ftyp, moov(size+8, size+8+6), mdat}, nil)
}

// videoTiming reads the video track's edit start, sample durations and
// media duration.
func videoTiming(t *testing.T, file []byte) (start int64, deltas []uint32, dur uint64) {
	t.Helper()
	boxes, err := topLevel(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range boxes {
		if b.typ != "moov" {
			continue
		}
		children(file[b.off+b.hdr:b.off+b.size], func(typ string, body []byte) error {
			if typ != "trak" {
				return nil
			}
			var h string
			var s int64
			var d []uint32
			var md uint64
			rewrite(body, func(typ string, body []byte) []byte {
				switch typ {
				case "hdlr":
					h = string(body[8:12])
				case "elst":
					s, _ = oneEdit(body)
				case "stts":
					d, _ = sampleDeltas(body)
				case "mdhd":
					_, md = fullTimes(body)
				}
				return body
			})
			if h == "vide" {
				start, deltas, dur = s, d, md
			}
			return nil
		})
	}
	return start, deltas, dur
}

func TestAlignEdits(t *testing.T) {
	for _, c := range []struct {
		name   string
		start  int32
		runs   [][2]uint32
		moved  float64
		start2 int64
		deltas []uint32
	}{
		// The keyframe is on screen at the cut: the media starts at it.
		{"first frame", 11, [][2]uint32{{1, 21}, {3, 20}}, 11.0 / 600, 0, []uint32{10, 20, 20, 20}},
		// A later frame is: it starts at the cut, the one before it
		// lasts longer (and the stts grows, moving the media data).
		{"later frame", 30, [][2]uint32{{4, 20}}, 10.0 / 600, 30, []uint32{30, 10, 20, 20}},
		// The cut is on a frame: nothing to do.
		{"on a frame", 40, [][2]uint32{{4, 20}}, 0, 40, []uint32{20, 20, 20, 20}},
	} {
		t.Run(c.name, func(t *testing.T) {
			file := trimmed(c.start, c.runs)
			path := filepath.Join(t.TempDir(), "trim.mp4")
			os.WriteFile(path, file, 0o644)
			moved, err := AlignEdits(path)
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(moved-c.moved) > 1e-9 {
				t.Errorf("moved %v, want %v", moved, c.moved)
			}
			out, _ := os.ReadFile(path)
			if c.moved == 0 && !bytes.Equal(out, file) {
				t.Error("an aligned file was rewritten")
			}
			start, deltas, dur := videoTiming(t, out)
			if start != c.start2 || !equal(deltas, c.deltas) {
				t.Errorf("edit starts at %d, samples %v; want %d, %v", start, deltas, c.start2, c.deltas)
			}
			// What plays from the edit's start is where it was.
			var sum uint64
			for _, d := range deltas {
				sum += uint64(d)
			}
			if dur != sum {
				t.Errorf("mdhd says %d, the samples last %d", dur, sum)
			}
			f, err := Read(bytes.NewReader(out), int64(len(out)))
			if err != nil || !f.FastStart() {
				t.Fatalf("%v %v", f.Top, err)
			}
			if a, _ := f.Track("soun"); a.Lead != 1024.0/48000 {
				t.Errorf("the audio's edit changed: %+v", a)
			}
			for i, o := range offsets(t, out) {
				want := []string{"AAAA", "BBBB"}[i]
				if got := string(out[o : o+4]); got != want {
					t.Errorf("chunk %d at %d reads %q, want %q", i, o, got, want)
				}
			}
			if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".*align*")); len(left) > 0 {
				t.Errorf("temporary files left: %v", left)
			}
		})
	}
}

func equal(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAlignEditsLeavesAlone(t *testing.T) {
	ctts := box("ctts", append(u32(0), u32(0)...))
	for name, file := range map[string][]byte{
		"composition offsets": trimmed(11, [][2]uint32{{1, 21}, {3, 20}}, ctts),
		"no edit to skip":     trimmed(0, [][2]uint32{{4, 20}}),
		"past the last frame": trimmed(70, [][2]uint32{{1, 20}, {1, 40}, {1, 5}}),
	} {
		path := filepath.Join(t.TempDir(), "trim.mp4")
		os.WriteFile(path, file, 0o644)
		if moved, err := AlignEdits(path); moved != 0 || err != nil {
			t.Errorf("%s: moved %v, %v", name, moved, err)
		}
		if out, _ := os.ReadFile(path); !bytes.Equal(out, file) {
			t.Errorf("%s: rewritten", name)
		}
	}
	file, _ := recording() // moov at the end
	path := filepath.Join(t.TempDir(), "rec.mp4")
	os.WriteFile(path, file, 0o644)
	if _, err := AlignEdits(path); err == nil {
		t.Error("no error for a file whose moov comes after its media")
	}
}
