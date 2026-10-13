# reel: notes for coding agents

reel records the screen on macOS — a whole display, one window or a drag-selected area — with system audio and a chosen microphone mixed into **one** audio track, and saves an MP4 that it copies to the clipboard as a file (paste into Slack). A regular Dock app: a main window to set up and list recordings, a small floating control while recording, pause/resume, and trim after stop.

It's Go on SDL3 (`github.com/Zyko0/go-sdl3`, purego, no cgo) with our own renderer, copied from an earlier project of the author's; the per-file provenance lines say which files. The recording pipeline calls ScreenCaptureKit, AVFoundation and CoreMedia through `purego/objc` in `internal/mac`.

- **Plan:** `docs/plan.md` (M0 scaffold + signing → M1 capture probe → M2 video writer → M3 audio mixer → M4 pause/stop → M5 app UI → M6 trim → M7 soak; each ends with an agent gate and a gate the user runs).
- **Status:** M0 passed (2026-10-13: both permissions granted to the signed Reel.app). M1 passed (2026-10-13): the agent gate, and the user's probes answered (a) a window filter hears only the window's own app (`-inf` while another app played), so window recordings take system audio from a second, audio-only display stream; (b) the mic is stamped on the host clock (lag in tens of ms, like system audio), so the mixer aligns by PTS; (c) not needed: the mixer emits on a host-clock tick either way; (d) not run: the stop path handles both (the stream erroring, or mic buffers stopping); M7's soak unplugs a mic. M2 passed (2026-10-13, agent gate; it has no user gate): `selftest -video-only` writes an H.264 High, yuv420p, BT.709-tagged, fast-start MP4 whose duration matches the recording to the millisecond, with every beacon flash 0.5 s apart and nothing dropped. Recordings write movie fragments every 5 s (killed at 12 s, 10 s could still be read; with network-use optimization alone the file was empty until the end) and `mp4.FastStart` moves the final moov to the front at Stop. A window is recorded title bar and all, so it's sized from its frame, not its content. M3 (audio) next.
- **What the probe found (`probe -self`, 2026-10-13):** a stream delivers on our queue, and its `synchronizationClock` is the host clock (−0.007 ms). Screen frames come at the FPS cap while the content changes, `420v`, and the first one was `complete` (no `started` seen with a window filter: the writer must accept both). System audio is 48 kHz stereo float32 planar in 960-frame (20 ms) buffers, and keeps coming while nothing plays (zeros: 50 buffers a second after the speakers closed), at 47 999.9 Hz with no PTS gaps; it arrives a median 61 ms after its PTS (max 80 ms), so the mixer needs a jitter window over 100 ms. **A covered window isn't drawn, so its capture is all `idle` frames with no pixels**: the beacon keeps itself on top, and recording a covered window of another app may freeze the same way. `CMSampleBufferGetAudioBufferListWithRetainedBlockBuffer` wants the list's exact size (a bigger one fails with −12737).

## Packages
| Package | What it does |
|---|---|
| `internal/mac` | The macOS bridge (purego/objc, never SDL): permissions (`ScreenAccess`, `MicAccess`, the requests), `Mics` (AVCaptureDevice: uniqueID = ScreenCaptureKit's `microphoneCaptureDeviceID`, transport), `ShareableContent` (displays with their pixel scale, other apps' windows). `StartStream`: one SCStream with the screen, system audio and a mic, delivered as `Sample`s (PTS, frame status, pixel buffer, audio format, PCM) to a handler on our serial queue (autorelease per sample) through a runtime class `ReelStreamOutput`; ScreenCaptureKit's protocols have no runtime objects until something refers to them, so `protocol` registers them. `Writer`: AVAssetWriter behind a state machine (H.264 settings checked with canApplyOutputSettings, appends only while writing, the hot path's messages pre-registered). Every selector, function, constant and protocol it uses is in a table in `mac.go`; `mac_test.go` checks them all against the installed frameworks without touching a device. |
| `internal/draw` | Display list (`draw.List`). Everything that draws emits one; `gfx` consumes it. Logical units, y down. |
| `internal/text` | Styles, runs and the go-text `Shaper`, the rasterizer, atlas and glyph cache. Embedded Inter and JetBrains Mono; system font fallback and Apple Color Emoji. |
| `internal/theme` | Color tokens (`Palette`, Light/Dark). Nothing hard-codes colors. |
| `internal/geom` | Path geometry the drawing code and the renderer agree on. |
| `internal/ui` | Input `Event`s, the `Widget` contract, the `Host`, focus layers, the `TextField`. Pure. |
| `internal/gfx` | GPU renderer: device and window, the uber-shader pipeline, glyph atlases, `Batch`, `RenderImage` (offscreen). |
| `internal/platform` | SDL loading and init, `QuitOnSignal`, `Wake`. |
| `internal/area` | The encoded size: points × scale, capped at H.264's 4096×2304 keeping the aspect, even sides. Pure. |
| `internal/timeline` | Host time to recording time: zero at the first frame, pauses as spans of host time, each sample placed by its own PTS (a late sample from before a pause is kept). Pure. |
| `internal/record` | A recording: the stream's handler retains frames with pixels into a bounded channel; one goroutine (locked thread, a pool per step) owns the timeline, the `mac.Writer` and the newest frame, which it writes again at the end and when a pause ends (frames only come on change). Fragments every 5 s, then `mp4.FastStart`. Calls `internal/mac`, never SDL. |
| `internal/mp4` | Reads an MP4's structure (top-level order, tracks, durations, edit lists) and `FastStart` (moov to the front, chunk offsets moved). Pure. |
| `internal/avsync` | Beacon flash and click onsets, and the audio − video offsets between them. Pure. |
| `internal/config`, `internal/logx` | Data, log and recordings paths (`REEL_DATA_DIR` overrides all three); a log file per subcommand plus crash output. |
| `shaders/` | HLSL sources in `src/`, compiled SPIR-V + MSL in `compiled/`, and `manifest.json`. |
| `cmd/reel` | One file per subcommand, registered in `init()`. Add a file; don't edit `main.go`. No subcommand is `app` (how the bundle starts). |

## Rules
- **Don't open the user's microphone from an agent.** Anything that records with a mic is the user's to run. Agents list devices (`devices` opens nothing and asks nothing) and, from M3, feed the mixer from WAV files.
- **Don't capture the user's screen from an agent.** Self-tests record only reel's own window. Don't print other apps' window titles into a transcript: `devices` lists them, so filter its output when an agent runs it.
- **Don't trigger permission prompts from an agent.** The requests only run from the app's buttons.
- **Never touch the user's real data.** Dev runs set `REEL_DATA_DIR` to `.dev/data` (the Makefile does, and `make run` passes it through `open --env`). Recordings go to `<data>/recordings`, not `~/Movies/Reel`. `reel` with no subcommand opens the app on the real paths, so never run it bare.
- **Run the app as the signed bundle (`make run`).** macOS gives permissions to the process that's responsible: a binary started from a terminal records with the terminal's permissions, and only `open` makes Reel.app its own. Dev bundles are signed with the user's Apple Development certificate (`SIGN`, its SHA-1) so permissions are kept against "identifier + certificate leaf" and survive rebuilds; `codesign -d -r- build/Reel.app` must show `certificate leaf`, never a `cdhash`. No hardened runtime in dev.
- **Objective-C exceptions kill the process.** purego can't catch them. Every selector, symbol, constant and protocol `internal/mac` uses goes in its tables (checked by `mac_test.go`); framework constants are read through `Dlsym`, never hard-coded; framework objects are kept in states where they can't throw (AVAssetWriter behind a state machine from M2).
- **Keep Go off real-time threads.** ScreenCaptureKit's sample handlers run on a dispatch queue we create, not an audio IO thread; they only classify and hand off. No AudioUnit render callbacks.
- **The core stays pure.** `internal/deps/purity_test.go` has two tiers: the pure packages import neither SDL nor purego nor `internal/mac`; `internal/mac` never imports SDL. Add new packages to its lists.
- **Only the main thread touches the GPU.** Other goroutines hand work over (a mutex-guarded value) and call `platform.Wake()`.
- **Running the app from an agent** opens a real window on the user's screen: keep runs short (`-exit-after`, stdio redirected: `</dev/null >/dev/null 2>&1`), run the bare `build/reel app` (it uses the terminal's permissions and asks nothing unless a button is clicked) and check nothing is left running. Prefer offscreen renders.
- **Graphics shaders use no storage buffers.** After editing `shaders/src`, run `make shaders` and commit the outputs and `manifest.json`.
- **Colors come from `theme.Palette`.** Sizes are logical units.
- **Exit checks are run, not asserted.** Look at what you built: render it and read the PNG; from M2, ffprobe what was recorded.
- **Copied code** keeps its one-line provenance comment.

## Commands
```
make build          # build/reel
make check          # vet + test + shader manifest + build
make app            # the signed build/Reel.app
make run            # make app, then open it (user only: it can ask for permissions)
make render         # offscreen PNG of the home scene -> build/render.png (ARGS="-dark", "-live")
./build/reel devices [-all]   # permissions, mics, displays, windows (opens nothing, asks nothing)
REEL_DATA_DIR=$PWD/.dev/data ./build/reel probe -self -for 6s </dev/null   # agents: records only its own flashing, clicking window (kept on top) and reports what arrives
./build/reel probe -display main -mic default -for 10s   # user only: a display and a mic (also -window ID, -app Chrome, -system-audio=false)
REEL_DATA_DIR=$PWD/.dev/data ./build/reel selftest -video-only -for 8s </dev/null   # agents: records the beacon to an MP4 and checks it (ffmpeg for the codec and frame checks)
./build/reel analyze [-flash-period 500ms] FILE.mp4   # dev: the same checks on any recording
REEL_DATA_DIR=$PWD/.dev/data ./build/reel app -exit-after 4s </dev/null >/dev/null 2>&1   # a short window run from an agent
codesign -d -r- build/Reel.app   # must say certificate leaf
```
Logs: `.dev/data/logs/<subcommand>.log`.

## Conventions
- **Commits:** conventional style (`feat(mac): …`), one per logical step. Push only when the user says (no remote yet).
