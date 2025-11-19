# Transcoder Architecture

## Overview

A TUI-based media transcoder that scans remote libraries via SSH/SFTP, queues files for transcoding, and manages the entire workflow with integrity verification.

## Components

```
┌─────────────────────────────────────────────────────────┐
│                    TUI (Bubble Tea)                     │
│  Dashboard │ Jobs │ Files │ Settings │ Logs            │
└─────────────────────┬───────────────────────────────────┘
                      │
┌─────────────────────┼───────────────────────────────────┐
│                     │                                   │
│  ┌─────────┐   ┌────▼────┐   ┌──────────┐              │
│  │ Scanner │   │ Worker  │   │ Encoder  │              │
│  │         │   │  Pool   │   │ (FFmpeg) │              │
│  └────┬────┘   └────┬────┘   └────┬─────┘              │
│       │             │              │                    │
│       └─────────────┼──────────────┘                    │
│                     │                                   │
│              ┌──────▼──────┐                            │
│              │   Database  │                            │
│              │  (SQLite)   │                            │
│              └─────────────┘                            │
└─────────────────────────────────────────────────────────┘
```

## Package Structure

```
transcoder-go/
├── cmd/transcoder/       # Main entry point
├── internal/
│   ├── config/           # YAML configuration
│   ├── database/         # SQLite schema & queries
│   ├── scanner/          # Remote library scanning (SSH/SFTP)
│   ├── transcode/        # Worker pool & FFmpeg encoding
│   ├── checksum/         # xxHash64/MD5 utilities
│   ├── tui/              # Bubble Tea TUI
│   └── types/            # Shared data types
└── docs/                 # Documentation
```

---

## Startup Workflow

### Startup Sequence

```mermaid
flowchart TD
    A[Start Application] --> B[Load config.yaml]
    B --> C{Config exists?}
    C -->|No| D[Use defaults]
    C -->|Yes| E[Parse config]
    D --> F[Open/Create SQLite DB]
    E --> F

    F --> G[Run schema migrations]
    G --> H[Recover orphaned jobs]

    H --> I{Orphaned jobs found?}
    I -->|Yes| J[Clear worker_id<br/>Keep status & progress]
    I -->|No| K[Create scanner]
    J --> K

    K --> L[Create worker pool]
    L --> M[Start N workers]
    M --> N[Launch TUI]

    N --> O[Workers begin claiming jobs]
```

### Scenarios

#### 1. Fresh Start (No Database)
- Creates new SQLite database at configured path
- Runs initial schema creation
- No files in `media_files` table
- No jobs to process
- **User action needed**: Press `s` to scan remote library

#### 2. Existing Database, No Active Jobs
- Opens existing database
- Runs migrations if schema version is outdated
- Workers start but find no queued jobs
- Workers poll every 2 seconds for new jobs
- **User action needed**: Press `a` to queue jobs for transcoding

#### 3. Existing Database with Queued Jobs
- Workers immediately start claiming queued jobs
- Jobs processed in priority order (highest first)
- Multiple workers can process different jobs concurrently

#### 4. Recovery After Crash (Orphaned Jobs)
Jobs in active states (`downloading`, `transcoding`, `uploading`) without a worker are recovered:

| Previous Status | Recovery Action |
|----------------|-----------------|
| `downloading` | Reset progress to 0, will re-download |
| `transcoding` | Keep progress, may resume from existing files |
| `uploading` | Keep progress, may resume from transcoded file |

### Worker Job Processing

When a worker claims a job, it checks for existing work:

```mermaid
flowchart TD
    A[Worker Claims Job] --> B[Check work directory<br/>~/.transcoder/work/job-ID/]

    B --> C{Downloaded file exists?}
    C -->|No| D[Download from remote]
    C -->|Yes| E[Skip download<br/>Calculate checksum]

    D --> F{Transcoded file exists?}
    E --> F

    F -->|No| G[Run FFmpeg transcode]
    F -->|Yes| H[Verify duration matches]

    H --> I{Duration OK?}
    I -->|No| J[Delete & re-transcode]
    I -->|Yes| K[Skip transcode<br/>Calculate checksum]

    G --> L[Validate & Upload]
    J --> G
    K --> L

    L --> M[Complete job]
    M --> N[Clean up work directory]
```

### Work Directory Structure

Default: `~/transcoder_temp/` (configurable via `workers.work_dir`)

```
~/transcoder_temp/
├── job-123/
│   ├── Movie.Name.2024.mkv           # Downloaded input
│   └── transcoded_Movie.Name.2024.mkv # Transcoded output
├── job-124/
│   └── ...
```

Files in work directories persist across restarts, enabling job resume.

### Database States on Startup

| Table | Fresh Start | After Scan | After Queue | After Transcode |
|-------|-------------|------------|-------------|-----------------|
| `media_files` | Empty | Populated with metadata | Unchanged | `should_transcode=0` for completed |
| `transcode_jobs` | Empty | Empty | Jobs with `status=queued` | Jobs with `status=completed` |
| `system_state` | `schema_version=2` | `last_scan_time` updated | Unchanged | Unchanged |

---

## Scanning Flow

```mermaid
flowchart TD
    A[Start Scan] --> B[Connect SSH/SFTP]
    B --> C[Detect checksum tool<br/>xxhsum or md5sum]
    C --> D[Walk directory tree]

    D --> E{Is video file?}
    E -->|No| D
    E -->|Yes| F[Check DB for existing file]

    F --> G{File exists in DB?}

    G -->|No| H[Run ffprobe<br/>extract metadata]
    H --> I[Add to DB<br/>no checksum yet]
    I --> D

    G -->|Yes| J{Size changed?}

    J -->|No| K{Has checksum?}
    K -->|Yes| L[Skip - unchanged]
    L --> D

    K -->|No| M[Backfill checksum]
    M --> D

    J -->|Yes| N[Run ffprobe<br/>extract metadata]
    N --> O[Calculate checksum]
    O --> P[Update DB]
    P --> D

    D --> Q{More files?}
    Q -->|Yes| E
    Q -->|No| R[Scan Complete]
```

---

## Transcoding Flow

```mermaid
flowchart TD
    A[Worker Claims Job] --> B[Create SSH connection]
    B --> C[Create work directory]

    C --> D[Stage 1: Download]
    D --> E[Download file via SFTP<br/>calculate checksum during transfer]

    E --> F[Stage 2: Transcode]
    F --> G[Run FFmpeg<br/>HEVC encoding with progress]

    G --> H[Stage 3: Validate]
    H --> I[Verify output with ffprobe]
    I --> J[Calculate output checksum]

    J --> K[Stage 4: Upload]
    K --> L[Upload to .transcoded temp file<br/>verify checksum matches]

    L --> M{Checksums match?}
    M -->|No| N[Fail job]

    M -->|Yes| O[Delete original file]
    O --> P[Rename temp to original]

    P --> Q[Mark job completed]
    Q --> R[Clean up work directory]

    N --> S[Clean up & retry later]
```

---

## Database Schema

### media_files
Discovered video files from remote library.

| Column | Type | Description |
|--------|------|-------------|
| id | INTEGER | Primary key |
| file_path | TEXT | Full remote path |
| file_name | TEXT | Filename only |
| file_size_bytes | INTEGER | File size |
| codec | TEXT | Video codec (h264, hevc, etc.) |
| resolution_width | INTEGER | Width in pixels |
| resolution_height | INTEGER | Height in pixels |
| duration_seconds | REAL | Video duration |
| bitrate_kbps | INTEGER | Bitrate |
| fps | REAL | Frames per second |
| should_transcode | BOOLEAN | Needs transcoding? |
| transcoding_priority | INTEGER | Queue priority |
| source_checksum | TEXT | xxHash64 or MD5 |
| source_checksum_algo | TEXT | Algorithm used |
| source_checksum_at | TIMESTAMP | When calculated |

### transcode_jobs
Active and completed transcoding jobs.

| Column | Type | Description |
|--------|------|-------------|
| id | INTEGER | Primary key |
| media_file_id | INTEGER | FK to media_files |
| status | TEXT | queued/downloading/transcoding/uploading/completed/failed/paused/canceled |
| stage | TEXT | download/transcode/validate/upload |
| progress | REAL | 0-100 percentage |
| worker_id | TEXT | Assigned worker |
| transcoded_file_size_bytes | INTEGER | Output size |
| encoding_time_seconds | INTEGER | Total encode time |
| encoding_fps | REAL | Encoding speed |
| local_input_checksum | TEXT | Downloaded file checksum |
| local_output_checksum | TEXT | Transcoded file checksum |
| uploaded_checksum | TEXT | Uploaded file checksum |
| checksum_verified | BOOLEAN | All checksums match |
| error_message | TEXT | Last error |
| retry_count | INTEGER | Number of retries |
| priority | INTEGER | Job priority |

### processing_log
Event log for debugging.

### system_state
Key-value store for app state (last scan time, schema version, etc.)

---

## Configuration

```yaml
remote:
  host: "media-server"
  port: 22
  user: "user"
  library_path: "/path/to/media"

local:
  database_path: "~/transcoder/transcoder.db"

workers:
  max_workers: 2
  work_dir: "~/transcoder_temp"

encoding:
  codec: "libx265"
  preset: "medium"
  crf: 23
  # Additional FFmpeg options...
```

---

## Key Design Decisions

### Checksum Strategy
- **First scan**: Skip checksums for speed (ffprobe only)
- **Subsequent scans**: Backfill checksums for unchanged files
- **Size comparison**: Fast change detection without reading entire file
- **Transfer verification**: Checksums calculated during download/upload

### Job Recovery & Resume
- Jobs survive application restart
- Orphaned jobs (downloading/transcoding/uploading) are recovered
- Workers re-claim orphaned jobs with priority
- **Stage skipping**: Jobs resume from where they left off:
  - Skip download if file already exists locally
  - Skip transcode if valid output file exists
  - Validates existing files before skipping

### Atomic File Replacement
1. Upload to `filename.transcoded` (temp)
2. Verify upload checksum matches local output
3. Delete original
4. Rename temp to original

### Worker Pool
- Configurable number of concurrent workers (0 to N)
- Each worker has dedicated SSH connection
- Progress updates via channel to TUI
- Pause/cancel support per job
- **Graceful scaling**: Workers complete current job before stopping when scaled down
- **Wind-down mode**: Set workers to 0 before shutdown/maintenance - active jobs complete, no new jobs claimed

### Progress Tracking
- **Download/Upload**: Byte-based progress (bytes transferred / total bytes)
- **Transcoding**: Time-based progress using `out_time_us` from ffmpeg (more reliable than frame count)

---

## Performance Optimizations

1. **Skip ffprobe for unchanged files** - DB lookup before expensive remote call
2. **Lazy checksum backfill** - First scan is fast, checksums fill in later
3. **Size-based change detection** - No need to read file contents
4. **Streaming checksum calculation** - Zero overhead during transfers
5. **No redundant verification** - Upload checksum verified during transfer, no re-read after
