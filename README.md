# Video Transcoder TUI

[![CI](https://github.com/juanitoe/transcoder/actions/workflows/ci.yml/badge.svg)](https://github.com/juanitoe/transcoder/actions/workflows/ci.yml)

A production-ready video transcoding system with an interactive Terminal UI for managing H.264 → HEVC conversions using Apple's VideoToolbox hardware acceleration.

<!-- Add screenshot here -->
<!-- ![Transcoder TUI Screenshot](docs/screenshot.png) -->

## Features

### Media Management
- **Remote Library Scanning** - Parallel scanning with SSH connection pooling (5-10x faster than serial)
- **Automatic Scheduled Scans** - Periodic scanning at configurable intervals with auto-queue option
- **Smart Change Detection** - Size-based detection with optional checksum verification
- **Priority Queue** - Manage transcoding order with configurable priorities (0-100)
- **Job Resume** - Continue interrupted jobs after restarts, skipping completed stages
- **Movies/TV Filtering** - Filter jobs by content type
- **Job Search** - Full-text search with bulk pause/cancel actions

### Performance
- **Hardware Acceleration** - VideoToolbox HEVC encoding on Apple Silicon
- **Concurrent Processing** - Worker pool with dynamic scaling (0-N workers)
- **Early Size Detection** - Skip transcoding if output would be larger than original
- **Pre-Transcode Estimation** - Estimate output size before full encode to avoid wasted effort
- **Minimum Savings Threshold** - Only transcode if estimated savings meet configured threshold
- **SSH Connection Pooling** - Configurable pool size (1-16) eliminates connection overhead
- **SFTP Buffer Optimization** - Tunable buffer sizes up to 1MB

### Monitoring & Control
- **Interactive TUI** - Real-time progress with color-coded stages
  - Blue: Download
  - Cyan: Transcode
  - Green: Upload
- **Five View Modes** - Dashboard, Jobs, Settings, Scanner, Logs
- **Job Control** - Pause, resume, cancel, force kill individual jobs
- **Bulk Actions** - Queue all eligible files, bulk pause/cancel from search
- **Live Bandwidth** - Per-transfer speed tracking
- **Progress Details** - FPS, speed multiplier, ETA, percentage

### Data Integrity
- **Checksum Verification** - xxHash64/MD5 with streaming calculation (skippable for speed)
- **Atomic File Replacement** - Upload to temp, verify, then swap
- **Duration Verification** - Prevent uploading truncated files
- **State Persistence** - SQLite database with WAL mode, survives crashes
- **Orphaned Job Recovery** - Automatic recovery of incomplete jobs
- **Retry Logic** - Auto-retry failed jobs up to 3 times

### Operations
- **Wind-down Mode** - Set workers to 0 to gracefully finish current jobs
- **Worker Scaling** - Add/remove workers on the fly via TUI
- **Keep Original Files** - Optional preservation of source files after transcoding
- **SSH Agent Support** - Works with ssh-agent for passphrase-protected keys
- **SSH Keepalive** - Configurable keepalive for long-lived connections
- **Flexible File Filtering** - Extensions, minimum size, exclude patterns, symlink handling

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
  ssh_pool_size: 4          # SSH connections for scanning (1-16)
  ssh_timeout: 30           # Connection timeout in seconds
  ssh_keepalive: 0          # Keepalive interval (0 = disabled)
  sftp_buffer_size: 32768   # SFTP buffer size in bytes (up to 1MB)

database:
  path: "~/transcoder/transcoder.db"

workers:
  max_workers: 2
  work_dir: "~/transcoder_temp"
  skip_checksum: false      # Skip verification for speed

encoder:
  codec: "hevc_videotoolbox"
  quality: 75               # 0-100, higher is better
  preset: "medium"          # ultrafast to veryslow
  min_expected_savings: 10  # Skip if less than 10% savings expected
  allow_larger: false       # Skip if output would be larger

files:
  extensions:               # File types to process
    - mkv
    - mp4
    - avi
  min_size: 0               # Minimum file size in bytes
  exclude_patterns: []      # Glob patterns to exclude
  follow_symlinks: false
  keep_original: false      # Keep source after transcoding

scanner:
  auto_scan_hours: 0        # Periodic scan interval (0 = disabled)
  auto_queue: false         # Auto-queue jobs after scan

logging:
  level: "info"             # debug, info, warn, error
  file: "~/transcoder/transcoder.log"
  scanner_log: "~/transcoder/scanner.log"
  max_size: 104857600       # Log rotation size (100MB)
```

### Usage

```bash
# Start the TUI
./transcoder

# Global shortcuts
[1-5]      Switch views (Dashboard, Jobs, Settings, Scanner, Logs)
[s]        Scan remote library
[r]        Refresh data
[h] or [?] Toggle help
[q]        Quit

# Jobs view
[a]        Add/queue jobs for transcoding
[p]        Pause selected job
[c]        Cancel selected job
[d]        Delete job (completed/failed/canceled only)
[K]        Force kill job
[f]        Filter by path (Movies/TV)
[/]        Search jobs
[Tab]      Switch between Active/Queued panels
[j/k]      Navigate up/down

# Settings view
[+/-]      Scale worker count
[Enter]    Edit setting
[Ctrl+S]   Quick save
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
- **Parallel Processing**: Worker pool with configurable SSH connections (1-16, default 4)
- **SSH Connection Pooling**: Reusable connections eliminate ~200ms overhead per file
- **Batch Database Operations**: Inserts/updates batched in groups of 20 (80-90% overhead reduction)
- **Automatic Scheduling**: Periodic scans at configurable intervals with optional auto-queue
- **First scan**: Fast metadata-only (checksums deferred to background)
- **Subsequent scans**: Lazy checksum backfill for unchanged files
- **Change detection**: Size comparison before expensive FFprobe
- **Smart filtering**: Only queue files needing transcoding
- **Progress tracking**: Live scan rates (files/sec, bytes/sec) and percentage completion

### Transcoding Pipeline
1. **Download** - SFTP transfer with streaming checksum
2. **Pre-check** - Estimate output size, skip if savings below threshold
3. **Transcode** - FFmpeg with VideoToolbox, real-time progress (FPS, speed, ETA)
4. **Validate** - Duration verification prevents truncated uploads
5. **Upload** - Atomic file replacement with checksum verification

### Job Management
- **Priority queue**: Jobs processed by priority score (0-100)
- **Retry logic**: Auto-retry up to 3 times before permanent failure
- **Full queue display**: View all queued jobs without limits
- **Search & filter**: Find jobs by filename, filter by Movies/TV
- **Bulk actions**: Queue all eligible files, bulk pause/cancel from search

### Worker Pool
- Dynamic scaling: Add/remove workers while running via TUI
- Graceful shutdown: Workers finish current job before stopping
- Wind-down mode: Set to 0 workers before maintenance
- Per-worker SSH connections for parallel transfers

### Logging
- **Application log**: Job lifecycle, errors, worker events
- **Scanner log**: SSH debug, ffprobe output, file discovery
- **Configurable levels**: debug, info, warn, error
- **Log rotation**: Configurable max size before rotation

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
