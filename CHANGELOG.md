# Changelog

## [0.3.8] - 2026-10-05


### Fixed
- `ListPipelineRuns` no longer deadlocks: it ran a per-run query while its outer result set was still open, which blocks forever on the single-connection SQLite pool. Outer rows are now drained and closed first (no other nested-query-while-rows-open sites exist).
- Every background goroutine is now bound to the module lifecycle: pipeline-run executors/finalizers (the 2s job poller), pool-job pollers, job workers, core dial, event-stream handlers and the scheduled scan loop run under a context cancelled by `Stop`, and `Stop` waits for them (bounded by its ctx) before closing the DB. Previously the pipeline poller kept running after `Stop` closed the DB and panicked on a nil DB. New background work is refused once `Stop` begins; if the `Stop` deadline expires with goroutines still running, the DB is left open rather than closed under them.
- Pipeline runs, pool pollers and local job workers no longer inherit the (short-lived) RPC context; they run under the module lifecycle context.

## [0.3.7] - 2026-10-05


### Added
- `integsupport` package exposing `Module`, `Config`, `NewTestModule`, and `Start` so the umbrella integration tests can drive the transcoder in-process (loopback listeners, temp SQLite DB, offline).

### Fixed
- `GetJob`/`ListJobs` no longer drop jobs whose `started_at`/`completed_at` are NULL (queued jobs).
- A cancelled job is no longer overwritten with `running`/`failed` by a racing worker; `Stop` waits for local workers before closing the DB and closes an Init-only gRPC listener.

## [0.3.5] - 2026-10-05

### Changed
- CI runs on GitHub-hosted runners from the umbrella template; retired-origin workflows removed.
- Dependencies resolve from published GitHub tags (no filesystem `replace`); requires core v0.6.0.

## [0.3.5] — 2026-09-08

### Added
- HLS playback remounts from `start=` (input `-ss`) so a household scrub past written segments starts a new playlist instead of 404ing.
- Segment GETs wait up to 20s for ffmpeg to write the first `.ts` of a remount.

## [0.3.3] — 2026-08-21

### Added

- **Full hardware encode support** for on-the-fly playback and offline jobs: NVENC, VAAPI, Intel QSV, AMD AMF, and Apple VideoToolbox.
- H.264, HEVC, and AV1 encoder selection per backend based on FFmpeg `-encoders` detection (AV1 only when the backend encoder is present).
- Platform-aware auto backend priority (e.g. VideoToolbox first on macOS, QSV/AMF prioritized on Windows).
- `TRANSCODER_QSV_DEVICE` for Intel QSV render node configuration.
- Playback `gpu` query values: `qsv`, `amf`, `videotoolbox` (plus existing `auto`, `software`, `nvenc`, `vaapi`).

### Changed

- Offline batch jobs now use the same hardware backend selection as playback (not NVENC-only).

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
