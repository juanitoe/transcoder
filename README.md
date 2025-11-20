# Video Transcoder TUI

A production-ready video transcoding system with an interactive Terminal UI for managing H.264 → HEVC conversions using Apple's VideoToolbox hardware acceleration.

<!-- Add screenshot here -->
<!-- ![Transcoder TUI Screenshot](docs/screenshot.png) -->

## Features

### 🎬 Media Management
- **Remote Library Scanning** - Scan and discover video files via SSH/SFTP
- **Smart Change Detection** - Size-based detection with optional checksum verification
- **Priority Queue** - Manage transcoding order with configurable priorities
- **Job Resume** - Continue interrupted jobs after restarts

### ⚡ Performance
- **Hardware Acceleration** - VideoToolbox HEVC encoding on Apple Silicon
- **Concurrent Processing** - Worker pool with dynamic scaling (0-N workers)
- **Bandwidth Monitoring** - Real-time transfer speed display
- **Stage Skipping** - Resume from where jobs left off (skip completed downloads/transcodes)

### 📊 Monitoring & Control
- **Interactive TUI** - Real-time progress with color-coded stages
  - Blue: Download
  - Cyan: Transcode
  - Green: Upload
- **Job Control** - Pause, resume, cancel individual jobs
- **Live Bandwidth** - Per-transfer speed tracking
- **Application Logging** - Structured logging with configurable levels

### 💾 Data Integrity
- **Checksum Verification** - xxHash64/MD5 with streaming calculation
- **Atomic File Replacement** - Upload to temp, verify, then swap
- **State Persistence** - SQLite database, survives crashes
- **Orphaned Job Recovery** - Automatic recovery of incomplete jobs

### 🔧 Operations
- **Wind-down Mode** - Set workers to 0 to gracefully finish current jobs
- **Worker Scaling** - Add/remove workers on the fly
- **Duration Verification** - Prevent uploading truncated files
- **SSH Key Support** - Works with ssh-agent for passphrase-protected keys

## Quick Start

### Requirements

- Go 1.21+
- FFmpeg with VideoToolbox support (macOS)
- SSH access to media server

### Installation

```bash
git clone https://github.com/yourusername/transcoder-go.git
cd transcoder-go
go build -o transcoder ./cmd/transcoder
```

### Configuration

Create `~/transcoder/config.yaml`:

```yaml
remote:
  host: "media-server.local"
  port: 22
  user: "your-user"
  ssh_key: "~/.ssh/id_rsa"
  media_paths:
    - "/path/to/movies"
    - "/path/to/tv-shows"

database:
  path: "~/transcoder/transcoder.db"

workers:
  max_workers: 2
  work_dir: "~/transcoder_temp"

encoder:
  codec: "hevc_videotoolbox"
  quality: 75  # 0-100, higher is better
  preset: "medium"

logging:
  level: "info"  # debug, info, warn, error
  file: "~/transcoder/transcoder.log"
  scanner_log: "~/transcoder/scanner.log"
```

### Usage

```bash
# Start the TUI
./transcoder

# Keyboard shortcuts
[s]  Scan remote library
[a]  Add all files to queue
[+]  Increase workers
[-]  Decrease workers
[p]  Pause selected job
[c]  Cancel selected job

# Navigate views
[1]  Dashboard
[2]  Jobs
[3]  Settings
[4]  Logs
```

## Architecture

See [ARCHITECTURE.md](docs/ARCHITECTURE.md) for detailed documentation including:
- Component diagrams
- Startup workflows
- Database schema
- Transcoding pipeline
- Performance optimizations

## Technology Stack

- **TUI**: [Bubbletea](https://github.com/charmbracelet/bubbletea) + Lipgloss
- **Database**: [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) (pure Go, no CGo)
- **SSH/SFTP**: [golang.org/x/crypto/ssh](https://pkg.go.dev/golang.org/x/crypto/ssh)
- **Checksums**: [xxHash64](https://github.com/cespare/xxhash)

## Features in Detail

### Scanning Strategy
- **Parallel Processing**: Worker pool architecture with configurable workers (default 4)
- **SSH Connection Pooling**: Reusable connections eliminate ~200ms overhead per file
- **Batch Database Operations**: Inserts/updates batched in groups of 20 (80-90% overhead reduction)
- **First scan**: Fast metadata-only (checksums deferred to background)
- **Subsequent scans**: Lazy checksum backfill for unchanged files
- **Change detection**: Size comparison before expensive FFprobe
- **Smart filtering**: Only queue files needing transcoding
- **Progress tracking**: Live scan rates (files/sec, bytes/sec) and percentage completion

### Transcoding Pipeline
1. **Download** - SFTP transfer with streaming checksum
2. **Transcode** - FFmpeg with VideoToolbox, time-based progress
3. **Validate** - Duration verification prevents truncated uploads
4. **Upload** - Atomic file replacement with checksum verification

### Worker Pool
- Dynamic scaling: Add/remove workers while running
- Graceful shutdown: Workers finish current job before stopping
- Wind-down mode: Set to 0 workers before maintenance
- Per-worker SSH connections for parallel transfers

### Logging
- **Application log**: Job lifecycle, errors, worker events
- **Scanner log**: SSH debug, ffprobe output, file discovery
- **Configurable levels**: debug, info, warn, error

## Performance

### Scanner Performance
- **5-10x faster** scanning on large libraries (5000+ files)
- Parallel FFprobe execution (4+ concurrent vs serial)
- No SSH process spawning overhead (pooled connections)
- Typical scan rate: 10-20 files/sec (vs 2-3 files/sec serial)

### Transcoding Performance
Typical performance on M2 Pro:
- 1080p → HEVC: ~100-150 fps (2-3x realtime)
- Size reduction: 40-50% average
- Download/Upload: Limited by network (typically 80-120 MB/s on gigabit)

## Contributing

This is a personal project but suggestions and bug reports are welcome via GitHub issues.

## License

MIT
