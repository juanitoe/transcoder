# Video Transcoder TUI

Go-based video transcoding system with interactive Terminal UI for managing H.264 → HEVC conversions on Apple M2 Pro hardware.

## Features

- 🎬 **Remote Library Scanning** - Discover video files from Plex server
- ⚡ **Hardware Acceleration** - VideoToolbox HEVC encoding on M2 Pro
- 🔄 **Parallel Processing** - Concurrent transcoding with worker pools
- 📊 **Interactive TUI** - Real-time progress monitoring with Bubbletea
- ⏸️ **Job Control** - Pause, resume, cancel individual jobs
- 📈 **Priority Queue** - Manage transcoding order
- 💾 **State Persistence** - SQLite database, resume on crash
- 🎯 **Single Binary** - No dependencies except FFmpeg

## Architecture

Built with:
- **TUI**: [Bubbletea](https://github.com/charmbracelet/bubbletea) + Bubbles
- **Database**: [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) (pure Go)
- **Worker Pool**: [alitto/pond](https://github.com/alitto/pond)
- **FFmpeg**: [u2takey/ffmpeg-go](https://github.com/u2takey/ffmpeg-go)
- **SFTP**: [pkg/sftp](https://github.com/pkg/sftp)

## Status

🚧 **In Development** - Phase 1: Core Infrastructure

- [x] Project structure
- [x] Core types defined
- [ ] Configuration management
- [ ] Database schema
- [ ] Remote scanner
- [ ] Transcoding engine
- [ ] TUI implementation

See [implementation plan](PLAN.md) for details.

## Requirements

- Go 1.21+
- FFmpeg with VideoToolbox support
- SSH access to Plex server

## Building

```bash
go build -o transcoder cmd/transcoder/main.go
```

## License

MIT
