# Changelog

## [0.3.3] — 2026-08-31

### Added

- **Full hardware encode support** for on-the-fly playback and offline jobs: NVENC, VAAPI, Intel QSV, AMD AMF, and Apple VideoToolbox.
- H.264, HEVC, and AV1 encoder selection per backend based on FFmpeg `-encoders` detection (AV1 only when the backend encoder is present).
- Platform-aware auto backend priority (e.g. VideoToolbox first on macOS, QSV/AMF prioritized on Windows).
- `TRANSCODER_QSV_DEVICE` for Intel QSV render node configuration.
- Playback `gpu` query values: `qsv`, `amf`, `videotoolbox` (plus existing `auto`, `software`, `nvenc`, `vaapi`).
- Playback HTTP hardening: loopback default bind, library path allowlists, optional `TRANSCODER_HTTP_TOKEN`, SSRF rejection for non-loopback URLs.
- Job and pipeline run resume after process restart; `CancelJob` works from SQLite without in-memory state.
- Throttled progress/fps persistence during transcode jobs.
- SQLite-backed settings for `ffmpeg_bin`, `max_concurrent`, and `scan_interval`.
- `replace` source disposition moves the first completed output onto the original path.
- FFprobe derived from FFmpeg path (`TRANSCODER_FFPROBE_BIN` override); probe failures fail closed.
- Pool gRPC dial honors mesh TLS unless `MUXCORE_INSECURE_DISABLE_TLS` / `MUXCORE_GRPC_INSECURE`.
- Remux pipeline action writes a sidecar file instead of mutating the source during filter eval.
- Forgejo CI: `golangci-lint` + `go test -race`.

### Changed

- Offline batch jobs now use the same hardware backend selection as playback (not NVENC-only).
- Default `TRANSCODER_HTTP_ADDR` is `127.0.0.1:9526` (was `:9526`).
- Removed unused `-progress pipe:1` from offline FFmpeg args (stderr `time=` parsing retained).
- `Dockerfile` exposes 9525/9526 (was 9520).

## [0.3.2] — 2026-08-21

### Added

- **On-the-fly playback transcoding** over HTTP (`TRANSCODER_HTTP_ADDR`, default `:9526`): fragmented MP4 stream for native media-ui playback.
- Software encoders (`libx264` / `libx265` / `libaom-av1`) and hardware paths (NVENC, VAAPI) with `gpu=auto|software|nvenc|vaapi`.
- `GET /api/playback/hardware` for encoder introspection.
- `TRANSCODER_MAX_PLAYBACK` concurrency limit for live sessions.

## [0.3.0] — 2026-08-20

### Added

- **Hold for review** (`hold_for_review` on setups): after transcode completes, destructive source dispositions (`replace`, `delete`, `archive`) pause in `pending_review` until an operator approves or rejects.
- gRPC `ApprovePipelineRun` / `RejectPipelineRun` — approve applies source disposition; reject removes transcoded outputs and keeps the original.
- Admin UI review queue on `/transcode` with approve/reject actions.

## [0.2.0] — 2026-08-20

### Added

- Tdarr-style transcode setups: library paths, filter stacks, multi-output pipelines, source disposition, scheduled/on-import processing.

## [0.1.5] — 2026-08-10

### Added

- Advertise `settings` capability so admin-ui discovers SettingsProvider without ListAll probing.

## [0.1.4] — 2026-08-10

### Added
- SettingsProvider mesh (`RegisterSettings`) for live `ffmpeg_bin`.

### Changed
- Pin `core/sdk/go/module` to **v0.5.2**.


## [0.1.3] — 2026-08-10

### Fixed
- Sync Info()/muxcore.json version to **0.1.3**.

## v0.1.1 (2026-08-10)

- Harden: default gRPC `:9525` (avoid collision with media-subtitles `:9520`); fix default `h264_fast` container typo (`mkx` → `mkv`)
- Advertise `transcoder` capability for seeded `media-transcode` workflow DAG
- Concurrent job slots via `TRANSCODER_MAX_CONCURRENT` (default 2)
- Health requires `ffmpeg` on PATH
- Optional MVP host/compose profile (`MVP_ENABLE_MEDIA_TRANSCODER=1` / `transcoder`)

## v0.1.0 (2026-07-28)

- Initial release: profiles, job queue, FFmpeg progress, NVENC/VAAPI detect
