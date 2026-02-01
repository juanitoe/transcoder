# Video Transcoder TUI

[![CI](https://github.com/juanitoe/transcoder/actions/workflows/ci.yml/badge.svg)](https://github.com/juanitoe/transcoder/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/juanitoe/transcoder)](https://github.com/juanitoe/transcoder/releases/latest)

Terminal UI for batch transcoding video files to HEVC with hardware acceleration.

Two modes:
- **Remote**: Scan files on NAS/server via SSH, download, transcode locally, upload back
- **Local**: Transcode files directly on the local machine

```
REMOTE MODE                                 LOCAL MODE

┌──────────────────────┐                    ┌──────────────────────┐
│    LOCAL MACHINE     │                    │    LOCAL MACHINE     │
│                      │                    │                      │
│  ┌────────────────┐  │                    │  ┌────────────────┐  │
│  │      TUI       │  │                    │  │      TUI       │  │
│  └───────┬────────┘  │                    │  └───────┬────────┘  │
│          │           │                    │          │           │
│  ┌───────▼────────┐  │                    │  ┌───────▼────────┐  │
│  │  Worker Pool   │  │                    │  │  Worker Pool   │  │
│  │                │  │                    │  │                │  │
│  │  ┌──────────┐  │  │                    │  │  ┌──────────┐  │  │
│  │  │  FFmpeg  │  │  │                    │  │  │  FFmpeg  │  │  │
│  │  └──────────┘  │  │                    │  │  └──────────┘  │  │
│  └───────┬────────┘  │                    │  └───────┬────────┘  │
│          │           │                    │          │           │
│      SSH/SFTP        │                    │     direct I/O       │
│          │           │                    │          │           │
└──────────┼───────────┘                    │  ┌───────▼────────┐  │
           │                                │  │  /movies       │  │
           ▼                                │  │  /tv-shows     │  │
┌──────────────────────┐                    │  └────────────────┘  │
│   REMOTE SERVER      │                    │                      │
│                      │                    └──────────────────────┘
│  /movies             │
│  /tv-shows           │
│                      │
└──────────────────────┘

Job Pipeline:
┌────────┐   ┌──────────┐   ┌───────────┐   ┌──────────┐   ┌────────┐
│ queued │──▶│ download │──▶│ transcode │──▶│ validate │──▶│ upload │──▶ done
└────────┘   └──────────┘   └───────────┘   └──────────┘   └────────┘
                  │               │               │             │
               (remote)       (always)        (always)      (remote)
                 only                                          only
```

## Features

- **Two modes**: Remote (SSH/SFTP) or local file processing
- Hardware encoding: VideoToolbox (macOS), VAAPI (Linux), libx265 (software)
- Parallel scanning with SSH connection pooling (remote mode)
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
- SSH access to media server (remote mode only)

## Configuration

Create `~/transcoder/config.yaml`:

### Remote Mode (default)

Transcode files from a remote server:

```yaml
mode: "remote"

remote:
  host: "media-server"
  port: 22
  user: "your-user"
  ssh_key: "~/.ssh/id_rsa"
  media_paths:
    - "/path/to/movies"
    - "/path/to/tv-shows"
  ssh_pool_size: 4

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

database:
  path: "~/transcoder/transcoder.db"

logging:
  level: "info"
  file: "~/transcoder/transcoder.log"
```

### Local Mode

Transcode files on the local machine:

```yaml
mode: "local"

local:
  media_paths:
    - "/path/to/movies"
    - "/path/to/tv-shows"

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

database:
  path: "~/transcoder/transcoder.db"

logging:
  level: "info"
  file: "~/transcoder/transcoder.log"
```

## Usage

```bash
./transcoder --version          # show version
./transcoder --validate-config  # check config and show summary
./transcoder --help             # show help
./transcoder                    # start TUI
./transcoder --daemon           # run in daemon mode (no TUI)
```

### Daemon Mode

Run without TUI for unattended server operation:

```bash
./transcoder --daemon
```

The daemon:
- Runs an initial scan on startup
- Auto-queues files for transcoding (if configured)
- Processes jobs with the worker pool
- Optionally re-scans at intervals
- Handles SIGTERM/SIGINT for graceful shutdown

Configure daemon behavior in `config.yaml`:

```yaml
scanner:
  auto_scan_interval_hours: 6    # Re-scan every 6 hours (0 = one scan only)
  auto_queue_after_scan: true    # Queue discovered files automatically
```

Example: run as a systemd service:

```ini
[Unit]
Description=Video Transcoder Daemon
After=network.target

[Service]
Type=simple
User=youruser
ExecStart=/path/to/transcoder --daemon
Restart=on-failure
RestartSec=10

[Install]
WantedBy=multi-user.target
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

### Remote Mode
1. Scan remote library via SSH, get metadata with ffprobe
2. Queue non-HEVC files
3. Download file via SFTP
4. Transcode with FFmpeg
5. Verify duration matches
6. Upload back, replace original

### Local Mode
1. Scan local directories, get metadata with ffprobe
2. Queue non-HEVC files
3. Transcode in place (to temp file)
4. Verify duration matches
5. Replace original

## License

MIT
