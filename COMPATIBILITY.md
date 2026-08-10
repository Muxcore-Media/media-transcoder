# Compatibility

## Core Version

| Module Version | Core Version | Status |
|----------------|-------------|--------|
| v0.1.1         | v0.4.0+     | Current |
| v0.1.0         | v0.4.0+     | Supported |

## Capabilities

| Capability | Notes |
|------------|-------|
| `media.transcoder` | Primary |
| `executor.transcode` | Alias |
| `transcoder` | Workflow DAG `media-transcode` capability ref |

## Breaking Changes

- Default listen address changed from `:9520` to `:9525` in v0.1.1 (override with `TRANSCODER_GRPC_ADDR`)
