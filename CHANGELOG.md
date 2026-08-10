# Changelog

## v0.1.1 (2026-08-10)

- Harden: default gRPC `:9525` (avoid collision with media-subtitles `:9520`); fix default `h264_fast` container typo (`mkx` → `mkv`)
- Advertise `transcoder` capability for seeded `media-transcode` workflow DAG
- Concurrent job slots via `TRANSCODER_MAX_CONCURRENT` (default 2)
- Health requires `ffmpeg` on PATH
- Optional MVP host/compose profile (`MVP_ENABLE_MEDIA_TRANSCODER=1` / `transcoder`)

## v0.1.0 (2026-07-28)

- Initial release: profiles, job queue, FFmpeg progress, NVENC/VAAPI detect
