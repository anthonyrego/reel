# reel: a Go screen recorder for macOS

## Context

The author shares screen recordings with co-workers often. macOS's built-in recorders (⌘⇧5 and QuickTime) can't record system audio together with a mic.

`reel` is a new Go desktop app at `~/projects/reel`:
- **Module and bundle:** `github.com/anthonyrego/reel` (public, MIT), bundle ID `com.anthonyrego.reel`.
- **What it records:** a whole display, one window, or a drag-selected area.
- **Audio:** system (loopback) audio and a chosen mic, mixed into **one AAC track** so any player plays both.
- **Output:** an MP4 saved locally, with the file copied to the clipboard to paste into Slack.

**v1 scope he chose:**
- In: a regular Dock window app (setup plus a recordings list), pause/resume, trim after stop.
- Out: upload, webcam, live mic meter.

**Stack:** the same as the author's earlier projects.
- Go, with SDL3 GPU through Zyko0/go-sdl3.
- Native calls through ebitengine/purego v0.10.2 `objc`, with no `import "C"`.
- The engine is copied from an earlier project, which carries an earlier project's engine plus the darwin bridges.

## Feasibility

It's buildable, and the hardest bridge already exists locally.

| Need | How | Prior art |
|---|---|---|
| Screen | ScreenCaptureKit `SCStream` `.screen` | an earlier project `internal/platform/screen_darwin.go` already drives SCK from Go (`shareableContent`, `ownApp`, blocks, pools, `describeError`) |
| System audio | Same stream, `.audio` (`capturesAudio`, 48 kHz stereo) | new |
| Chosen mic | Same stream, `.microphone`: `captureMicrophone` + `microphoneCaptureDeviceID` = AVCaptureDevice `uniqueID`, macOS 15.0+ (SCStream.h:23-31, 355-362). He's on 15.7.3 | new |
| Mix to one track | Pure-Go mixer on a 48 kHz timeline built from PTS | an earlier project `internal/pcm`, `internal/echo/follow.go` (envelope correlation for sync checks) |
| Encode | `AVAssetWriter`: hardware H.264 plus AAC | new |
| ObjC from Go | purego `RegisterClass` with protocols, `NewBlock`, struct args (CMTime and CGRect verified in purego's arm64 code) | an earlier project, in production |
| UI, hotkey, overlay | Engine: draw/text/theme/gfx/platform/ui. Carbon hotkey. SDL3View runtime subclass | an earlier project `hotkey_darwin.go`, `overlay_darwin.go`; its `settings.go` toggle/segmented, `menu.go` dropdown, `confirm.go` |

**Rejected: `SCRecordingOutput`**, ScreenCaptureKit's built-in file writer.
- It gives no control over audio track layout.
- Apple's forums have an unresolved report of corrupt MP4s when `captureMicrophone=true` (thread 805892).
- It doesn't support HDR. That's not why we drop it, but it's another limit.

**Rejected: a Swift sidecar or cgo.** An earlier project has already proven purego against SCK, AVFAudio and CoreAudio.

## Pipeline design (SDK-verified, critique folded in)

```
SCStream (one per recording; filter = display-excluding-self | window | display+sourceRect)
 ├─ .screen ─┐   SCK serial queue (autorelease = WORK_ITEM): classify only,
 ├─ .audio  ─┼─▶ CFRetain video sbuf / copy PCM into Go → bounded channels (drop+count on overflow)
 └─ .microphone┘
                     ▼
        writer goroutine (LockOSThread, pool per iteration, 20 ms tick) — sole owner of:
        timeline (pause intervals in host time, Map(pts)) · mixer · AVAssetWriter state machine
                     ▼
        MP4: H.264 (avc1, High AutoLevel, no B-frames, 2 s keyframes, sRGB/709) + AAC 48k stereo 160 kbps
        shouldOptimizeForNetworkUse = YES (moov first, so Slack and Chrome start fast)
```

**Rules that keep the output correct.** Each comes from a concrete failure the design review found.

1. **Frame filter.** Accept `SCFrameStatus` Complete(0) **or Started(4)**, and only buffers with a non-NULL image buffer. The first frame is Started; idle frames carry no pixels.
2. **Timeline.**
   - The timeline starts at zero: `startSession(kCMTimeZero)`, and T0 is the PTS of the first frame.
   - Audio output PTS is exactly `n/48000`.
   - Pause is a list of host-time intervals. **Every buffer is mapped by its PTS, not by when it arrived**, because mic buffers arrive late.
3. **Last-frame re-append.** Keep the newest frame retained. At resume and at stop, re-append it retimed, then call `endSession(stopTime)`. SCK only sends frames when the screen changes, so a static screen would otherwise show the wrong frame or end early.
4. **Mixer.**
   - **Source adapters** branch on the ASBD: 1–4 channels, 16/24/48 kHz, int or float, interleaved or not. They take channel 0 from mic arrays.
   - **Resampler:** a pure-Go variable-ratio resampler. A PI loop drives it from (position expected from PTS − position written), clamped to ±0.1%. It hard re-anchors only above 50 ms.
   - **Emission** runs off the host-clock tick, up to `now − 150 ms`, and fills gaps with silence. SCK may send no `.audio` while nothing plays, and the mic may vanish.
   - **Output:** per-source gain, then a soft limiter, then 1024-frame blocks.
   - **No SDL resampler.** It holds samples internally, so PTS can't be mapped to an exact sample position.
5. **Memory hand-off.** Build audio sample buffers with `CMBlockBufferCreateWithMemoryBlock(NULL, …, kCMBlockBufferAssureMemoryNowFlag)` + `CMBlockBufferReplaceDataBytes`. **Never hand Go memory to CoreMedia.** CFRelease everything after appending, because the writer retains its own copy.
6. **No ObjC exceptions.** They're fatal, and purego can't catch them. Prevent them by construction:
   - a Go writer state machine: Configured → Writing → Session → Finishing → Done;
   - `canApplyOutputSettings`/`canAddInput` before `addInput`;
   - append only while the writer status is Writing;
   - every Dlsym'd `NSString` constant checked for nil when it loads.
7. **Encoding limits.**
   - Cap the encoded frame at 4096×2304 (H.264 level 5.2). A HiDPI 4K panel is 5120×2880 or more, which exceeds it; the MacBook's own 3456×2234 fits.
   - Use even dimensions, 30 fps by default, SDR only.
   - Size the bitrate so a 30-minute recording stays under 1 GB, and warn above that.
8. **Area capture.**
   - `sourceRect` is in display-local points: subtract `SCDisplay.frame.origin`.
   - Snap the rect to the pixel grid and set `preservesAspectRatio=NO`.
   - One display only.
   - The selection overlay is **not** click-through, unlike an earlier project's overlay.
9. **Hot path.** Pre-register typed `objc_msgSend` functions. purego's `objc.Send[T]` calls `RegisterFunc` on every call, which allocates.
10. **Stops and errors.**
    - These all finalize the file through the normal stop path: UserStopped −3817 (the menu-bar control), SystemStoppedStream −3821 (sleep or lid), window close, and `didStopWithError`.
    - `NSWorkspaceWillSleep` triggers an automatic pause.
11. **Warnings, no fixes in v1.**
    - Speakers plus the built-in mic records system audio twice, as an echo. Detect it with an earlier project's CoreAudio transport check, show a hint, and offer the "system audio off" toggle. No echo cancellation.
    - A Bluetooth mic forces the headset into its low-quality call mode. Warn when the device's `transportType` is Bluetooth.

**Open questions M1 must answer before the writer is built:**
- **(a)** Does window capture only get the owning app's audio? If so, add a second audio-only `SCStream` with a display filter that excludes self. Its PTS share the host clock, so the mixer doesn't change.
- **(b)** Are mic PTS and system PTS both host time? Log PTS − host clock for each type.
- **(c)** Does `.audio` deliver buffers during silence?
- **(d)** Does unplugging the mic error the stream, or just stop mic buffers?

## Package layout (the author's earlier projects' conventions)

```
cmd/reel/         one file per subcommand, registered in init(); main.go untouched
  app devices probe record selftest micsync trim render analyze(dev-only: ffprobe/ffmpeg)
internal/  (tier 1 PURE: no SDL, no purego — purity_test)
  timeline/ mixer/ session/ (state machine over Capture/Writer/Clock interfaces, fakes in tests)
  area/ (snap, even dims, pixel cap) mp4check/ (box walker) avsync/ (onset detection)
  library/ (~/Movies/Reel, sidecar JSON) settings/ + copied engine: draw text theme geom config logx ui
  ui/{mainview,hud,areaselect,trimview}
internal/mac/  (tier 2: purego only, no SDL; *_other.go stubs)
  objcx (SELs, typed msgSend, pools, NSString/NSURL/NSError, CF retain/release, constant table)
  dispatch sck cmedia writer devices export imagegen pasteboard workspace
  bridge_test.go — every selector/symbol/protocol/constant resolves; arm64 CMTime-in-callback test
internal/gfx, internal/platform  (SDL: windows, HUD flags, overlay, hotkey, Wake)
```

**Rough size**, my estimate excluding the copied engine:
- the bridge, about 1.5k lines;
- the timeline, mixer and session with their tests, about 1.5k;
- the UI, about 2–2.5k;
- the self-test and analysis code, about 0.6k.

The mixer's drift loop and the bridge are where the real difficulty is. The UI is mostly assembly from an earlier project's parts.

**Copy sources** (each copied file keeps its one-line provenance comment):
- **From an earlier project:**
  - the engine packages;
  - `platform` with `Wake`, the hotkey and the overlay;
  - from `screen_darwin.go`: the patterns of `shareableContent`, `ownApp`, `copyImage` and `describeError`;
  - `coreaudio_darwin.go`, for the output-transport check.
- **From that project:**
  - `scripts/make-app.sh`, `launch.go` (`bundleOf`) and `applock_unix.go`;
  - the `settings.go` toggle and segmented control, `menu.go` and `confirm.go`, generalized into `internal/ui/controls`.

## Build, signing, TCC

- go.mod `go 1.26.5` / `toolchain go1.26.8` (the same as the siblings; GOTOOLCHAIN=auto fetches it).
- `make app` runs `make-app.sh`, which writes the Info.plist:
  - `LSMinimumSystemVersion 15.0`, `NSMicrophoneUsageDescription` (without it the process is killed when it touches the mic), `NSHighResolutionCapable`;
  - then `codesign --force -s $(SIGN)`, with `SIGN` (a certificate's SHA-1) from a git-ignored `local.mk`.
- Use an Apple Development certificate, by its SHA-1 (`security find-identity -v -p codesigning`): two certificates can share a name. Without one, the bundle is signed ad hoc and macOS asks for the permissions again after every build.
- No hardened runtime in dev. With it, the mic needs an entitlement.
- **Gate:** `codesign -d -r- build/Reel.app` shows `certificate leaf`, not a `cdhash`. That's what makes Screen Recording and mic grants survive rebuilds.
- `make run` = `make app && open build/Reel.app`. Running the bare binary would put the permissions on Terminal.
- **Expect:** a relaunch after the first Screen Recording grant, and Sequoia's periodic re-confirmation for SCK apps. `SCContentSharingPicker` is the v2 way out.
- **Dev data:** `REEL_DATA_DIR=.dev/data`. The real `~/Movies/Reel` is never touched from dev or agent runs.

**AGENTS.md rules**: an earlier project's rules, plus:
- Agents never open the mic.
- Agents never capture his screen. Self-tests capture only reel's own SDL window and its own clicks, with `excludesCurrentProcessAudio=NO`.
- Exit checks are run, not asserted.
- Conventional commits, one per step. Push only when he says.

## Milestones (each ends with a gate; "agent" gates use no mic and no user screen)

| # | Build | Agent gate | His gate |
|---|---|---|---|
| **M0** Scaffold + signing | repo, engine copy, Makefile, purity test (2 tiers), bundle + signing, `reel devices` (displays, windows, mics with uniqueID and transport, permission state; no mic opened), bridge_test | `make check` passes; `codesign -d -r-` shows `certificate leaf`; `devices` lists mics | `make run`, grant, rebuild, `make run`: **no new prompt** |
| **M1** Capture probe | `reel probe`, logging only: buffer counts per type, frame-status histogram, ASBDs, PTS − host clock, audio during silence | `probe -self 6s`: first frame is Started, audio buffers arrive, clock offset < 1 ms | `probe -display -mic <id>` and `probe -window Chrome` while another app plays. **Answers (a)–(d)** |
| **M2** Video writer | writer state machine, timeline, frame filter, re-append at stop, sRGB/709, pixel cap, mp4check | `selftest -video-only 8s`: moov before mdat; h264 High; yuv420p; even dims; duration ± 1 frame; flash count matches. Plus the `movieFragmentInterval` + `kill -9` experiment (keep it only if the partial file plays) | — |
| **M3** Audio | mixer + resampler + PI loop, AAC input, PCM synthetic tests (±300 ppm over 30 min within 2 ms; jitter; gaps; late arrival) | `selftest 20s`: click-to-flash offset median ≤ 40 ms, spread ≤ 8 ms; audio length = video length ± 50 ms. `record -self -mic-wav … -mic-ppm 200` exercises the mic path without a device | `micsync` (offset per transport: built-in vs Bluetooth); a 60 s recording narrating over YouTube, checked in QuickTime and in Slack |
| **M4** Pause/stop | pause intervals, re-append at resume, stop/error/sleep paths | self-test pausing at 4–7 s while the colour changes: 17 s ± 1 frame; no clicks inside the pause; new colour after resume; A/V offset moves ≤ 5 ms. Kill the stream mid-recording: the file still plays | — |
| **M5** App UI | main window (target segmented control, display picker, window list, area overlay, mic dropdown, system-audio toggle, warnings), HUD (timer/pause/stop), global hotkey, library list (copy/reveal/open/delete), auto-copy on stop | `reel render` goldens for each state; `app -exit-after` run, log read | record all 3 target types on both displays, paste into Slack, play it on a co-worker's machine |
| **M6** Trim | `AVAssetExportSession` passthrough + `timeRange` (Objective-C API; the new async API is Swift-only), one scrub frame via the image generator (CMTime callback arg = pointer on arm64; deprecated synchronous `copyCGImageAtTime` as the fallback) | `trim -from 2 -to 5`: 3.00 ± 0.04 s; A/V offset kept; extra lead-in reported with `-ignore_editlist 1` | trim in the UI, paste into Slack |
| **M7** Soak | — | `selftest 30m`: RSS grows < 1 MB/min; A/V drift ≤ 10 ms | 30 min with the mic: unplug the USB mic, close the recorded window, stop from Control Center, let the display sleep. Each must leave a playable file |

**Execution:**
- M0 → M1 run in sequence on main.
- After M1, two worktree tracks, like an earlier project:
  - **P (pipeline):** M2 → M3 → M4.
  - **U (UI):** M5, against the `session` interface with fakes.
- M6 and M7 follow on main.
- I merge at gates, and he runs the "his gate" column.

**Cut from v1:**
- capture visuals: click highlighting (`showMouseClicks` needs BGRA), HDR, HEVC, a filmstrip (one scrub frame instead);
- picking and starting: the system picker, window thumbnails in the picker, a countdown, areas that span displays;
- audio: echo cancellation, a manual mic-offset slider (only if `micsync` shows > 100 ms);
- crash recovery, unless M2's experiment gives it for free.

## Verification (end to end)

1. **Each milestone:** `make check` (vet, tests, purity, bridge_test, build). Then that milestone's agent gate:
   - `reel selftest` analysed by `reel analyze` (ffprobe/ffmpeg, dev-only; pure `avsync` does onset detection);
   - `mp4check` checks the file structure.
2. **His run:**
   - `make run`, pick each target type, mic + system audio, record about 60 s while a video plays and he narrates;
   - pause once, stop;
   - check that the file appears in the list and on the clipboard;
   - paste into Slack;
   - a co-worker plays it in browser and desktop Slack: both voices audible, in sync, the colours right.
3. **Soak:** M7's 30-minute run and its resilience checks.
