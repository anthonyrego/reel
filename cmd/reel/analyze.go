package main

// analyze: reads back what a recording wrote, for self-tests and gates.
// It checks the file's structure itself (mp4check) and asks ffprobe and
// ffmpeg (Homebrew, dev only: reel never ships them) for the codec and for
// the decoded frames and samples. Nothing here runs in the app.

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/anthonyrego/reel/internal/avsync"
	"github.com/anthonyrego/reel/internal/mp4"
)

func init() {
	register(&command{name: "analyze", summary: "dev: check a recording's structure, codec and beacon flashes (needs ffmpeg)", run: runAnalyze})
}

func runAnalyze(args []string) error {
	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	period := fs.Duration("flash-period", 0, "the beacon's flash period, to count flashes (0: don't)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: reel analyze [-flash-period 500ms] FILE.mp4")
	}
	c := newChecks(os.Stdout)
	analyzeFile(c, fs.Arg(0), expect{period: period.Seconds()})
	return c.result()
}

// expect is what a self-test knows the file should hold.
type expect struct {
	duration  float64 // seconds (0: unknown)
	frameDur  float64 // one frame, the duration tolerance
	period    float64 // the beacon's flash period (0: don't count)
	videoOnly bool
}

// checks prints PASS/FAIL lines and remembers whether any failed.
type checks struct {
	out    io.Writer
	failed int
}

func newChecks(out io.Writer) *checks { return &checks{out: out} }

func (c *checks) check(ok bool, name, format string, args ...any) {
	mark := "PASS"
	if !ok {
		mark = "FAIL"
		c.failed++
	}
	fmt.Fprintf(c.out, "%s  %-22s %s\n", mark, name, fmt.Sprintf(format, args...))
}

func (c *checks) note(name, format string, args ...any) {
	fmt.Fprintf(c.out, "      %-22s %s\n", name, fmt.Sprintf(format, args...))
}

func (c *checks) result() error {
	if c.failed > 0 {
		return fmt.Errorf("%d checks failed", c.failed)
	}
	return nil
}

// analyzeFile runs every check that applies to the file.
func analyzeFile(c *checks, path string, want expect) {
	f, err := mp4.ReadFile(path)
	if err != nil {
		c.check(false, "structure", "%v", err)
		return
	}
	c.check(f.FastStart(), "moov before mdat", "%s", strings.Join(f.Top, " "))
	v, hasVideo := f.Track("vide")
	_, hasAudio := f.Track("soun")
	c.check(hasVideo, "video track", "%d tracks", len(f.Tracks))
	if want.videoOnly {
		c.check(!hasAudio, "no audio track", "audio %v", hasAudio)
	}
	if hasVideo && want.duration > 0 {
		tol := math.Max(want.frameDur, 0.001)
		c.check(math.Abs(v.Duration-want.duration) <= tol, "video duration",
			"%.3f s (recorded %.3f s, ±%.3f)", v.Duration, want.duration, tol)
	}

	if _, err := exec.LookPath("ffprobe"); err != nil {
		c.note("ffprobe", "not installed: codec and frame checks skipped (brew install ffmpeg)")
		return
	}
	s, err := probeVideo(path)
	if err != nil {
		c.check(false, "ffprobe", "%v", err)
		return
	}
	c.check(s.Codec == "h264" && s.Profile == "High", "codec", "%s %s", s.Codec, s.Profile)
	c.check(s.PixFmt == "yuv420p", "pixel format", "%s", s.PixFmt)
	c.check(s.Width%2 == 0 && s.Height%2 == 0 && s.Width > 0, "size", "%d×%d", s.Width, s.Height)
	c.check(s.Primaries == "bt709" && s.Transfer == "bt709" && s.Space == "bt709", "color tags",
		"primaries %s, transfer %s, matrix %s", s.Primaries, s.Transfer, s.Space)

	if want.period <= 0 {
		return
	}
	ps, err := luma(path)
	if err != nil {
		c.check(false, "frames", "%v", err)
		return
	}
	onsets := avsync.Onsets(ps, avsync.Midway(ps), want.period/2)
	c.note("frames", "%d decoded, luma %.0f…%.0f", len(ps), minV(ps), maxV(ps))
	if want.duration > 0 {
		expected := want.duration / want.period
		c.check(math.Abs(float64(len(onsets))-expected) <= 1.5, "flash count",
			"%d flashes in %.2f s (about %.1f expected)", len(onsets), want.duration, expected)
	}
	var gaps []float64
	for i := 1; i < len(onsets); i++ {
		gaps = append(gaps, onsets[i]-onsets[i-1])
	}
	if g := avsync.Summarize(gaps); g.N > 0 {
		tol := 1.5 * math.Max(want.frameDur, 1.0/30)
		c.check(math.Abs(g.Median-want.period) <= tol, "flash spacing",
			"median %.3f s, %.3f…%.3f (period %.3f)", g.Median, g.Min, g.Max, want.period)
	}
}

func minV(ps []avsync.Point) float64 {
	m := math.Inf(1)
	for _, p := range ps {
		m = math.Min(m, p.V)
	}
	return m
}

func maxV(ps []avsync.Point) float64 {
	m := math.Inf(-1)
	for _, p := range ps {
		m = math.Max(m, p.V)
	}
	return m
}

// videoStream is what ffprobe says about the video.
type videoStream struct {
	Codec     string `json:"codec_name"`
	Profile   string `json:"profile"`
	PixFmt    string `json:"pix_fmt"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Primaries string `json:"color_primaries"`
	Transfer  string `json:"color_transfer"`
	Space     string `json:"color_space"`
}

func probeVideo(path string) (videoStream, error) {
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name,profile,pix_fmt,width,height,color_primaries,color_transfer,color_space",
		"-of", "json", path).Output()
	if err != nil {
		return videoStream{}, fmt.Errorf("ffprobe: %w", err)
	}
	var r struct{ Streams []videoStream }
	if err := json.Unmarshal(out, &r); err != nil {
		return videoStream{}, err
	}
	if len(r.Streams) == 0 {
		return videoStream{}, errors.New("ffprobe: no video stream")
	}
	return r.Streams[0], nil
}

// luma is each video frame's mean brightness at its presentation time,
// decoding every frame as stored (no frame-rate conversion).
func luma(path string) ([]avsync.Point, error) {
	times, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "frame=pts_time", "-of", "csv=p=0", path).Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe frames: %w", err)
	}
	var ts []float64
	for _, line := range strings.Fields(string(times)) {
		t, err := strconv.ParseFloat(strings.TrimSuffix(line, ","), 64)
		if err != nil {
			return nil, fmt.Errorf("frame time %q: %w", line, err)
		}
		ts = append(ts, t)
	}
	const w, h = 16, 10
	raw, err := exec.Command("ffmpeg", "-v", "error", "-i", path, "-map", "0:v:0", "-fps_mode", "passthrough",
		"-vf", fmt.Sprintf("scale=%d:%d,format=gray", w, h), "-f", "rawvideo", "-").Output()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg: %w", err)
	}
	n := len(raw) / (w * h)
	if n != len(ts) {
		return nil, fmt.Errorf("ffmpeg decoded %d frames, ffprobe listed %d", n, len(ts))
	}
	ps := make([]avsync.Point, n)
	for i := range n {
		var sum int
		for _, b := range raw[i*w*h : (i+1)*w*h] {
			sum += int(b)
		}
		ps[i] = avsync.Point{T: ts[i], V: float64(sum) / (w * h)}
	}
	return ps, nil
}

// pcmMono decodes the first audio track to mono float32 at rate.
func pcmMono(path string, rate int) ([]float32, error) {
	raw, err := exec.Command("ffmpeg", "-v", "error", "-i", path, "-map", "0:a:0", "-ac", "1", "-ar", strconv.Itoa(rate),
		"-f", "f32le", "-").Output()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg audio: %w", err)
	}
	out := make([]float32, len(raw)/4)
	for i := range out {
		out[i] = math.Float32frombits(uint32(raw[4*i]) | uint32(raw[4*i+1])<<8 | uint32(raw[4*i+2])<<16 | uint32(raw[4*i+3])<<24)
	}
	return out, nil
}
