# Compatibility

## Core Version

| Module Version | Core Version | Status |
|----------------|-------------|--------|
| v0.3.3         | v0.4.0+     | Current |
| v0.3.2         | v0.4.0+     | Supported |
| v0.3.0         | v0.4.0+     | Supported |
| v0.2.0         | v0.4.0+     | Supported |
| v0.1.1         | v0.4.0+     | Supported |
| v0.1.0         | v0.4.0+     | Supported |

## Capabilities

| Capability | Notes |
|------------|-------|
| `media.transcoder` | Primary |
| `executor.transcode` | Alias |
| `transcoder` | Workflow DAG `media-transcode` capability ref |
| `settings` | Admin-ui settings provider (`ffmpeg_bin`, `max_concurrent`, `scan_interval`) |

## Breaking Changes

- Default playback HTTP bind changed from `:9526` to `127.0.0.1:9526` in v0.3.3 (override with `TRANSCODER_HTTP_ADDR`; compose may still use `:9526`)
- Default gRPC listen address changed from `:9520` to `:9525` in v0.1.1 (override with `TRANSCODER_GRPC_ADDR`)
