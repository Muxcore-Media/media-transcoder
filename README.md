# Media Transcoder

**FFmpeg video transcoding with profile management, job queue, progress tracking, and optional NVIDIA NVENC acceleration.**

MuxCore sidecar module (`media-transcoder`) exposing `muxcore.transcoder.v1.TranscodeService` over gRPC.

---

## How It Works

```
Enqueue(input, output, profile) ──→ FFmpeg job ──→ output file
         │                              │
         ├── SQLite profiles + jobs     ├── progress / fps (stderr parse)
         └── DetectHardware (nvenc/vaapi advertised)
```

### Key Features

- **Profile CRUD** — video/audio codec, preset, CRF, max width/height, GPU flag, container
- **Job queue** — enqueue, cancel, list (paged), get by ID; concurrent slots via `TRANSCODER_MAX_CONCURRENT`
- **Default profiles** — `h264_fast` (H.264 Fast), `hevc_gpu` (HEVC GPU, max height 1080)
- **Hardware detection** — reports NVENC / VAAPI encoders when FFmpeg lists them
- **GPU encode path** — when `use_gpu` and NVENC are available: `h264_nvenc` / `hevc_nvenc` / `av1_nvenc`; otherwise libx264 / libx265 / libaom-av1
- **Workflow** — capability `transcoder` for seeded `media-transcode` DAG (with `media-ffprobe`)

---

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `TRANSCODER_DB_PATH` | `/var/lib/media-transcoder/transcoder.db` | SQLite database path |
| `TRANSCODER_GRPC_ADDR` | `:9525` | gRPC listen address (also reported as `HTTPAddr`) |
| `TRANSCODER_MAX_CONCURRENT` | `2` | Max simultaneous FFmpeg jobs |
| `MUXCORE_GRPC_ADDR` | — | Core mesh address (optional) |
| `MUXCORE_INSECURE_DISABLE_TLS` | `false` | Disable TLS for local/dev mesh |

Module info: ID `media-transcoder`, version `0.1.1`, capabilities `media.transcoder` / `executor.transcode` / `transcoder`, role `transcoder`, min core `0.4.0`.

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

---

## gRPC API

Proto: `proto/transcodev1/transcoder.proto`

| RPC | Purpose |
|-----|---------|
| `ListProfiles` / `CreateProfile` / `UpdateProfile` / `DeleteProfile` | Transcode profiles |
| `Enqueue` / `CancelJob` / `ListJobs` / `GetJob` | Job lifecycle |
| `DetectHardware` | Available hardware encoders |

---

## Development

```bash
go test -race -count=1 ./...
```
