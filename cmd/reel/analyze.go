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
	"time"

	"github.com/anthonyrego/reel/internal/avsync"
	"github.com/anthonyrego/reel/internal/mp4"
	"github.com/anthonyrego/reel/internal/record"
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
	// refresh is the display's refresh interval: a flash's onset in the
	// video wobbles by up to that much.
	refresh float64
	// micAfter is where a fake mic's clicks fall after each flash (0:
	// no fake mic).
	micAfter float64
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
	if !want.videoOnly {
		analyzeAudio(c, path, f, onsets, want)
	}
}

// Audio sync limits.
const (
	// maxSyncOffset is how far the picture may trail a sound made with it:
	// a frame and a half at 30 fps.
	maxSyncOffset = 0.045
	// maxSyncDrift is how far the offset may move from the first third of
	// the recording to the last.
	maxSyncDrift = 0.010
	// maxMicDrift is how far the fake mic may move against the system
	// clicks over the recording: both are audio, sample-exact, so only
	// placement errors (and the speakers' own clock) move it.
	maxMicDrift = 0.003
)

// analyzeAudio checks the audio track: its format and length, and, from
// the decoded mix, that every flash's click is there and that a fake
// mic's clicks land where their PTS put them, beside their flashes and
// against the system clicks, from the start to the end.
func analyzeAudio(c *checks, path string, f mp4.File, flashes []float64, want expect) {
	a, ok := f.Track("soun")
	c.check(ok, "audio track", "%d tracks", len(f.Tracks))
	if !ok {
		return
	}
	_ = a
	checkAudioLength(c, path)
	s, err := probeAudio(path)
	if err != nil {
		c.check(false, "ffprobe audio", "%v", err)
		return
	}
	c.check(s.Codec == "aac" && s.Rate == "48000" && s.Channels == 2, "audio format",
		"%s %s Hz, %d channels", s.Codec, s.Rate, s.Channels)
	pcm, err := pcmMono(path, 48000)
	if err != nil {
		c.check(false, "decode audio", "%v", err)
		return
	}
	clicks := avsync.AudioOnsets(pcm, 48000, 0.02, 0.1)
	// A click near a flash is the beacon's (system audio); the rest are
	// the fake mic's.
	var system, mic []float64
	for _, at := range clicks {
		near := false
		for _, v := range flashes {
			if math.Abs(at-v) <= want.period/4 {
				near = true
				break
			}
		}
		if near {
			system = append(system, at)
		} else {
			mic = append(mic, at)
		}
	}
	// The beacon's own clicks: heard, and how late its speakers play them
	// (a property of the test, not of the recording: noted, not judged).
	sys := avsync.Summarize(avsync.Pair(flashes, system, want.period/4))
	c.note("clicks", "%d heard: %d with a flash, %d between", len(clicks), len(system), len(mic))
	c.check(sys.N >= len(flashes)-2, "every flash heard", "%d of %d flashes have a click", sys.N, len(flashes))
	if sys.N > 0 {
		c.note("speaker latency", "the beacon's clicks come %+.0f ms after its flashes (p5 %+.0f, p95 %+.0f)",
			1000*sys.Median, 1000*sys.P5, 1000*sys.P95)
	}
	if want.micAfter <= 0 {
		return
	}

	// The fake mic is stamped with the exact host time each click "was
	// captured", micAfter past a flash's due time, so it measures the
	// recording itself: a click against its flash (audio PTS vs video
	// PTS), and against the system click before it (two audio sources).
	shifted := make([]float64, len(flashes))
	for i, v := range flashes {
		shifted[i] = v + want.micAfter
	}
	av := avsync.Pair(shifted, mic, want.period/4)
	o := avsync.Summarize(av)
	c.check(o.N >= len(flashes)-3, "fake mic heard", "%d of %d", o.N, len(flashes))
	if o.N == 0 {
		return
	}
	// A screen shows a frame a refresh or more after it's drawn, and the
	// video's PTS is when it's shown, so the picture trails the click by
	// that much (the same is true of a real mic and a real screen).
	c.check(o.Median >= -maxSyncOffset && o.Median <= 0.010, "A/V sync",
		"mic click − flash %+.1f ms from where they were made together (p5 %+.1f, p95 %+.1f)",
		1000*o.Median, 1000*o.P5, 1000*o.P95)
	// A flash's onset is only seen at a captured frame: every 1/fps, or
	// every refresh when the display is slower.
	grain := math.Max(want.refresh, want.frameDur)
	c.check(o.P95-o.P5 <= grain+0.008, "A/V spread",
		"%.1f ms (frames come every %.1f ms)", 1000*(o.P95-o.P5), 1000*grain)
	if d, ok := thirds(av); ok {
		c.check(math.Abs(d) <= maxSyncDrift, "A/V drift", "%+.1f ms from the first third to the last", 1000*d)
	}
	// Each mic click against the system click before it: both audio,
	// sample-exact, so only placement moves it.
	var gaps []float64
	for _, m := range mic {
		prev := math.Inf(-1)
		for _, s := range system {
			if s <= m && s > prev {
				prev = s
			}
		}
		if !math.IsInf(prev, -1) {
			gaps = append(gaps, m-prev)
		}
	}
	if d, ok := thirds(gaps); ok {
		c.check(math.Abs(d) <= maxMicDrift, "mic drift", "%+.2f ms against the system clicks, first third to last", 1000*d)
	}
}

// thirds is the median of the last third of xs minus that of the first.
func thirds(xs []float64) (float64, bool) {
	n := len(xs) / 3
	if n < 3 {
		return 0, false
	}
	return avsync.Summarize(xs[len(xs)-n:]).Median - avsync.Summarize(xs[:n]).Median, true
}

// audioStream is what ffprobe says about the audio.
type audioStream struct {
	Codec    string `json:"codec_name"`
	Rate     string `json:"sample_rate"`
	Channels int    `json:"channels"`
}

func probeAudio(path string) (audioStream, error) {
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "a:0",
		"-show_entries", "stream=codec_name,sample_rate,channels", "-of", "json", path).Output()
	if err != nil {
		return audioStream{}, fmt.Errorf("ffprobe: %w", err)
	}
	var r struct{ Streams []audioStream }
	if err := json.Unmarshal(out, &r); err != nil {
		return audioStream{}, err
	}
	if len(r.Streams) == 0 {
		return audioStream{}, errors.New("ffprobe: no audio stream")
	}
	return r.Streams[0], nil
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

// analyzePause checks a self-test recording with one pause: it's cut out
// whole, the picture right after it is the one on screen at the resume
// (the beacon turned still and blue during the pause), every click stays
// on its 0.5 s beat through the cut, and sound and picture keep the same
// offset on both sides of it.
func analyzePause(c *checks, res record.Result, pauseFor time.Duration, period, frameDur float64) {
	path := res.Path
	f, err := mp4.ReadFile(path)
	if err != nil {
		c.check(false, "structure", "%v", err)
		return
	}
	c.check(f.FastStart(), "moov before mdat", "%s", strings.Join(f.Top, " "))
	c.check(len(res.Pauses) == 1, "one pause", "%d cut out", len(res.Pauses))
	if len(res.Pauses) != 1 {
		return
	}
	p := res.Pauses[0].Seconds()
	c.check((res.PausedFor-pauseFor).Abs() <= 30*time.Millisecond, "pause length",
		"%.3f s cut out at %.3f s (paused for %.3f s)", res.PausedFor.Seconds(), p, pauseFor.Seconds())
	v, okV := f.Track("vide")
	a, okA := f.Track("soun")
	if !okV || !okA {
		c.check(false, "tracks", "video %v, audio %v", okV, okA)
		return
	}
	c.check(math.Abs(v.Duration-res.Duration.Seconds()) <= frameDur, "video duration",
		"%.3f s (recorded %.3f s)", v.Duration, res.Duration.Seconds())
	_ = a
	checkAudioLength(c, path)

	blue := func(rgb [3]float64) bool { return rgb[2] > 150 && rgb[0] < 80 && rgb[1] < 120 }
	for _, s := range []struct {
		name string
		at   float64
		want bool
	}{
		{"before the pause", p - 0.25, false},
		{"just after it", p + 0.2, true},
		{"still after it", p + pauseStill.Seconds() - 0.4, true},
	} {
		rgb, err := frameColor(path, s.at)
		if err != nil {
			c.check(false, s.name, "%v", err)
			continue
		}
		c.check(blue(rgb) == s.want, s.name, "rgb %.0f,%.0f,%.0f at %.2f s (blue wanted: %v)", rgb[0], rgb[1], rgb[2], s.at, s.want)
	}

	pcm, err := pcmMono(path, 48000)
	if err != nil {
		c.check(false, "decode audio", "%v", err)
		return
	}
	clicks := avsync.AudioOnsets(pcm, 48000, 0.02, 0.1)
	worst := 0.0
	for i := 1; i < len(clicks); i++ {
		worst = math.Max(worst, math.Abs(clicks[i]-clicks[i-1]-period))
	}
	c.check(len(clicks) > 1 && worst <= 0.03, "click spacing",
		"%d clicks, each %.0f ms apart give or take %.1f ms (none from inside the pause)", len(clicks), 1000*period, 1000*worst)
	expected := v.Duration / period
	c.check(math.Abs(float64(len(clicks))-expected) <= 1.5, "click count", "%d (about %.1f expected)", len(clicks), expected)

	ps, err := luma(path)
	if err != nil {
		c.check(false, "frames", "%v", err)
		return
	}
	flashes := avsync.Onsets(ps, avsync.Midway(ps), period/2)
	var before, after []float64
	for _, t := range flashes {
		switch {
		case t < p:
			before = append(before, t)
		case t > p+pauseStill.Seconds():
			after = append(after, t)
		}
	}
	b := avsync.Summarize(avsync.Pair(before, clicks, period/4))
	af := avsync.Summarize(avsync.Pair(after, clicks, period/4))
	if b.N < 3 || af.N < 3 {
		c.check(false, "A/V across the pause", "%d flashes heard before, %d after", b.N, af.N)
		return
	}
	c.check(math.Abs(af.Median-b.Median) <= 0.010, "A/V across the pause",
		"click − flash %+.1f ms before (%d), %+.1f ms after (%d)", 1000*b.Median, b.N, 1000*af.Median, af.N)
}

// frameColor is the mean color of the frame on screen at t seconds: the
// last one presented at or before t. (ffmpeg -ss t gives the first frame
// at or after t, which across a still stretch of a recording is the wrong
// one: frames come only when the screen changes.)
func frameColor(path string, t float64) ([3]float64, error) {
	times, err := frameTimes(path)
	if err != nil {
		return [3]float64{}, err
	}
	shown := ""
	for _, s := range times {
		if v, _ := strconv.ParseFloat(s, 64); v <= t {
			shown = s
		}
	}
	if shown == "" {
		return [3]float64{}, fmt.Errorf("no frame by %.3f s", t)
	}
	raw, err := exec.Command("ffmpeg", "-v", "error", "-ss", shown, "-i", path,
		"-frames:v", "1", "-vf", "scale=8:8", "-f", "rawvideo", "-pix_fmt", "rgb24", "-").Output()
	if err != nil {
		return [3]float64{}, fmt.Errorf("ffmpeg: %w", err)
	}
	if len(raw) < 3 {
		return [3]float64{}, errors.New("ffmpeg: no frame")
	}
	var sum [3]float64
	for i := 0; i+2 < len(raw); i += 3 {
		for k := range 3 {
			sum[k] += float64(raw[i+k])
		}
	}
	n := float64(len(raw) / 3)
	return [3]float64{sum[0] / n, sum[1] / n, sum[2] / n}, nil
}

// playedDuration is how long a stream plays ("v:0", "a:0"), as ffprobe
// reads it through the edit list: an AAC track also holds the encoder's
// priming samples (about 44 ms), which the edit list skips.
func playedDuration(path, stream string) (float64, error) {
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", stream,
		"-show_entries", "stream=duration", "-of", "csv=p=0", path).Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe: %w", err)
	}
	return strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
}

// checkAudioLength checks the audio plays as long as the video.
func checkAudioLength(c *checks, path string) {
	a, errA := playedDuration(path, "a:0")
	v, errV := playedDuration(path, "v:0")
	if errA != nil || errV != nil {
		c.check(false, "audio duration", "%v %v", errA, errV)
		return
	}
	c.check(math.Abs(a-v) <= 0.035, "audio duration", "plays %.3f s (video %.3f s)", a, v)
}

// frameTimes are the video frames' presentation times, as ffprobe prints
// them, in order.
func frameTimes(path string) ([]string, error) {
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "frame=pts_time", "-of", "csv=p=0", path).Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe frames: %w", err)
	}
	var ts []string
	for _, f := range strings.Fields(string(out)) {
		ts = append(ts, strings.TrimSuffix(f, ","))
	}
	return ts, nil
}
