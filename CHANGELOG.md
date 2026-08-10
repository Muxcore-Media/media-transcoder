# Changelog

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
