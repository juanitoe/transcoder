# Video Transcoder TUI

[![CI](https://github.com/juanitoe/transcoder/actions/workflows/ci.yml/badge.svg)](https://github.com/juanitoe/transcoder/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/juanitoe/transcoder)](https://github.com/juanitoe/transcoder/releases/latest)

Terminal UI for batch transcoding video files to HEVC. Scans remote libraries via SSH, transcodes locally with hardware acceleration, uploads back.

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              LOCAL MACHINE                                  │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │                           TUI (Bubble Tea)                            │  │
│  │          Dashboard │ Jobs │ Settings │ Scanner │ Logs                 │  │
│  └───────────────────────────────────────────────────────────────────────┘  │
│                                    │                                        │
│          ┌─────────────────────────┼─────────────────────────┐              │
│          ▼                         ▼                         ▼              │
│  ┌──────────────┐        ┌──────────────────┐       ┌──────────────┐        │
│  │   Scanner    │        │   Worker Pool    │       │   Database   │        │
│  │              │        │   (N workers)    │       │   (SQLite)   │        │
│  └──────┬───────┘        └────────┬─────────┘       └──────────────┘        │
│         │                         │                                         │
│         │                ┌────────┴────────┐                                │
│         │                ▼                 ▼                                │
│         │          ┌──────────┐    ┌──────────────┐                         │
│         │          │ Encoder  │    │   Checksum   │                         │
│         │          │ (FFmpeg) │    │ Verification │                         │
│         │          └──────────┘    └──────────────┘                         │
│         │                                                                   │
└─────────┼───────────────────────────────────────────────────────────────────┘
          │              ▲                          │
          │ SSH/SFTP     │ ffprobe                  │ SFTP
          │ scan         │ metadata                 │ download/upload
          ▼              │                          ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                            REMOTE SERVER (NAS)                              │
│                                                                             │
│    /movies                    /tv-shows                                     │
│    ├── Movie1.mkv (H.264)     ├── Show1/                                    │
│    ├── Movie2.mp4 (HEVC) ✓    │   ├── S01E01.mkv                            │
│    └── Movie3.avi (H.264)     │   └── S01E02.mkv                            │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘

Job Pipeline:
┌─────────┐    ┌───────────┐    ┌───────────┐    ┌──────────┐    ┌──────────┐
│ queued  │───▶│ download  │───▶│ transcode │───▶│ validate │───▶│  upload  │───▶ completed
└─────────┘    └───────────┘    └───────────┘    └──────────┘    └──────────┘
                    │                 │                               │
                    ▼                 ▼                               ▼
                 failed            failed                          failed
```

## Features

- Parallel remote scanning with SSH connection pooling
- Hardware encoding: VideoToolbox (macOS), VAAPI (Linux), libx265 (software)
- Worker pool with runtime scaling
- Skip files that would grow larger after transcoding
- Checksum verification (xxHash64)
- Job queue with pause/resume/cancel
- SQLite state persistence

## Install

Download from [releases](https://github.com/juanitoe/transcoder/releases/latest):

```bash
# Linux x86_64
curl -L https://github.com/juanitoe/transcoder/releases/latest/download/transcoder-linux-amd64 -o transcoder

# Linux ARM64
curl -L https://github.com/juanitoe/transcoder/releases/latest/download/transcoder-linux-arm64 -o transcoder

# macOS Apple Silicon
curl -L https://github.com/juanitoe/transcoder/releases/latest/download/transcoder-darwin-arm64 -o transcoder

# macOS Intel
curl -L https://github.com/juanitoe/transcoder/releases/latest/download/transcoder-darwin-amd64 -o transcoder

chmod +x transcoder
```

Or build from source:

```bash
git clone https://github.com/juanitoe/transcoder.git
cd transcoder
go build -o transcoder ./cmd/transcoder
```

## Requirements

- FFmpeg with HEVC encoder (`hevc_videotoolbox`, `hevc_vaapi`, or `libx265`)
- SSH access to media server (for remote mode)

## Configuration

Create `~/transcoder/config.yaml`:

```yaml
remote:
  host: "media-server"
  port: 22
  user: "your-user"
  ssh_key: "~/.ssh/id_rsa"
  media_paths:
    - "/path/to/movies"
    - "/path/to/tv-shows"
  ssh_pool_size: 4

database:
  path: "~/transcoder/transcoder.db"

workers:
  max_workers: 2
  work_dir: "~/transcoder_temp"

encoder:
  codec: "hevc_videotoolbox"  # or hevc_vaapi, libx265
  quality: 75
  preset: "medium"

files:
  extensions: [mkv, mp4, avi]
  keep_original: false

logging:
  level: "info"
  file: "~/transcoder/transcoder.log"
```

## Usage

```bash
./transcoder --version
./transcoder
```

**Shortcuts:**

| Key | Action |
|-----|--------|
| `1-5` | Switch views (Dashboard, Jobs, Settings, Scanner, Logs) |
| `s` | Scan library |
| `a` | Queue jobs |
| `p` | Pause job |
| `c` | Cancel job |
| `+/-` | Scale workers |
| `q` | Quit |

## How it works

1. Scan remote library via SSH, get file metadata with ffprobe
2. Queue non-HEVC files for transcoding
3. Download file via SFTP
4. Transcode with FFmpeg
5. Verify duration matches
6. Upload back, replace original

## License

MIT
