# Media Transcoder

**FFmpeg video transcoding with profile management, job queue, progress tracking, and multi-vendor hardware acceleration (NVENC, VAAPI, QSV, AMF, VideoToolbox).**

MuxCore sidecar module (`media-transcoder`) exposing `muxcore.transcoder.v1.TranscodeService` over gRPC.

---

## How It Works

```
Enqueue(input, output, profile) ──→ FFmpeg job ──→ output file
         │                              │
         ├── SQLite profiles + jobs     ├── progress / fps (stderr parse, persisted)
         └── DetectHardware (NVENC / VAAPI / QSV / AMF / VideoToolbox)
```

### Key Features

- **Profile CRUD** — video/audio codec, preset, CRF, max width/height, GPU flag, container
- **Tdarr-style setups** — library path rules, multi-output pipelines, filter/action stacks, source disposition (`keep` / `replace` / `delete` / `archive`), optional **hold for review** before destructive disposition
- **Step templates** — `ListStepTemplates` / `ApplyStepTemplate` for reusable filter/action packs
- **Pipeline runs** — history, skip reasons, per-output job linkage; jobs and pipeline runs resume after restart
- **Auto processing** — `on_import` via mesh events (`media.movie.file.added`, `media.tv.episode.file.added`, `media.file.imported`)
- **Library scan** — `ScanSetups` RPC + scheduled loop (`TRANSCODER_SCAN_INTERVAL`, default 6h; persisted via settings)
- **Job queue** — enqueue, cancel, list (paged), get by ID; concurrent slots via `TRANSCODER_MAX_CONCURRENT` (persisted via settings)
- **Optional pool dispatch** — forward jobs to `media-transcoder-pool` when `TRANSCODER_USE_POOL=1`
- **Default profiles** — `h264_fast` (H.264 Fast), `hevc_gpu` (HEVC GPU, max height 1080)
- **Hardware detection** — reports NVENC, VAAPI, QSV, AMF, and VideoToolbox encoders when FFmpeg lists them
- **GPU encode path** — when `use_gpu` and hardware is available: NVENC / VAAPI / QSV / AMF / VideoToolbox for H.264, HEVC, and AV1 (per-encoder availability); otherwise libx264 / libx265 / libaom-av1
- **On-the-fly playback transcoding** — HTTP `127.0.0.1:9526` streams fragmented MP4 to browsers (software or any detected hardware backend); consumed by `media-ui-app` via BFF proxy
- **Trickplay sprites** — `GET /stream/trickplay` generates cached scrub-preview JPEG sheets
- **Workflow** — capability `transcoder` for seeded `media-transcode` DAG (with `media-ffprobe`)

---

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `TRANSCODER_DB_PATH` | `/var/lib/media-transcoder/transcoder.db` | SQLite database path |
| `TRANSCODER_GRPC_ADDR` | `:9525` | gRPC listen address |
| `TRANSCODER_HTTP_ADDR` | `127.0.0.1:9526` | Playback/trickplay HTTP listen address (compose may override to `:9526`) |
| `TRANSCODER_HTTP_TOKEN` | — | Optional shared token for `/stream/transcode` and `/stream/trickplay` |
| `TRANSCODER_LIBRARY_PATHS` | — | Comma-separated allowed file prefixes for playback `src` paths (also uses setup library paths) |
| `TRANSCODER_FFMPEG_BIN` | `ffmpeg` | FFmpeg executable (persisted via admin settings) |
| `TRANSCODER_FFPROBE_BIN` | derived from FFmpeg path | FFprobe executable for pipeline filters |
| `TRANSCODER_MAX_PLAYBACK` | `4` | Max simultaneous on-the-fly playback transcode sessions |
| `TRANSCODER_VAAPI_DEVICE` | `/dev/dri/renderD128` | VAAPI render node for hardware playback encode |
| `TRANSCODER_QSV_DEVICE` | same as VAAPI device | Intel QSV render node (`-init_hw_device qsv=hw@…`) |
| `TRANSCODER_MAX_CONCURRENT` | `2` | Max simultaneous FFmpeg jobs (persisted via admin settings) |
| `TRANSCODER_SCAN_INTERVAL` | `6h` | Scheduled library scan interval (persisted via admin settings) |
| `TRANSCODER_USE_POOL` | `0` | When `1`/`true`, enqueue via `media-transcoder-pool` |
| `TRANSCODER_POOL_ADDR` | mesh discovery | Explicit pool gRPC address (overrides discovery) |
| `TRANSCODER_TRICKPLAY_DIR` | `<db-dir>/trickplay` | Cached trickplay sprite directory |
| `MUXCORE_GRPC_ADDR` | — | Core mesh address (optional) |
| `MUXCORE_INSECURE_DISABLE_TLS` | `false` | Disable TLS for local/dev mesh and pool dial |
| `MUXCORE_GRPC_INSECURE` | `false` | Alias for insecure mesh/pool dial |

Module info: ID `media-transcoder`, version `0.3.3`, capabilities `media.transcoder` / `executor.transcode` / `transcoder` / `settings`, role `transcoder`, min core `0.4.0`.

Optional MVP host: `MVP_ENABLE_MEDIA_TRANSCODER=1` (compose profile `transcoder`).

---

## Quick Start

```bash
go build -o media-transcoder ./cmd/module

export MUXCORE_INSECURE_DISABLE_TLS=true
./media-transcoder --muxcore-mesh-addr localhost:9090

# List profiles
grpcurl :9525 muxcore.transcoder.v1.TranscodeService/ListProfiles

# Enqueue a job
grpcurl -d '{"input_path":"/path/in.mkv","output_path":"/path/out.mkv","profile_id":"h264_fast"}' \
  :9525 muxcore.transcoder.v1.TranscodeService/Enqueue

# Detect hardware
grpcurl :9525 muxcore.transcoder.v1.TranscodeService/DetectHardware
```

Requires `ffmpeg` on `PATH` (Health fails closed without it).

### On-the-fly playback (media-ui)

When transcoding is enabled in playback policy and direct play is not preferred, the media UI BFF proxies:

```
GET /stream/transcode?src=/stream/movies/{id}
  → GET http://127.0.0.1:9526/stream/transcode?src=http://127.0.0.1:9430/stream/movies/{id}
```

Query parameters on the transcoder endpoint:

| Param | Description |
|-------|-------------|
| `src` | Required. Loopback `http(s)` URL or absolute file path under configured library prefixes |
| `profile` | Transcode profile id (default `h264_fast`) |
| `gpu` | `auto`, `software`, `nvenc`, `vaapi`, `qsv`, `amf`, or `videotoolbox` |
| `token` | Required when `TRANSCODER_HTTP_TOKEN` is set |

Hardware introspection: `GET /api/playback/hardware`

Trickplay scrub preview:

```
GET /stream/trickplay?src=http://127.0.0.1:9430/stream/movies/{id}&duration=3600&interval=10
```

---

## gRPC API

Proto: `proto/transcodev1/transcoder.proto`

| RPC | Purpose |
|-----|---------|
| `ListProfiles` / `CreateProfile` / `UpdateProfile` / `DeleteProfile` | Transcode profiles |
| `Enqueue` / `CancelJob` / `ListJobs` / `GetJob` | Job lifecycle |
| `DetectHardware` | Available hardware encoders |
| `ListSetups` / `GetSetup` / `UpsertSetup` / `DeleteSetup` | Tdarr-style pipeline setups |
| `ListStepTemplates` / `ApplyStepTemplate` | Reusable pipeline step packs |
| `ProcessFile` / `ScanSetups` | Manual or bulk library processing |
| `ListPipelineRuns` / `GetPipelineRun` | Pipeline audit/history |
| `ApprovePipelineRun` / `RejectPipelineRun` | Review queue — apply or discard destructive disposition |

---

## Development

```bash
go test -race -count=1 ./...
```
