package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"transcoder/internal/database"
	"transcoder/internal/scanner"
	"transcoder/internal/transcode"
	"transcoder/internal/types"
)

// Messages

type tickMsg time.Time
type autoScanTickMsg time.Time
type progressMsg types.ProgressUpdate
type scanCompleteMsg struct{ autoQueued int } // autoQueued > 0 if jobs were auto-queued
type scanProgressMsg scanner.ScanProgress
type errorMsg string

// Commands

// tickCmd returns a command that sends a tick message every second
func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// autoScanTickCmd returns a command that sends a tick for auto-scanning
func autoScanTickCmd(intervalHours int) tea.Cmd {
	if intervalHours <= 0 {
		return nil
	}
	return tea.Tick(time.Duration(intervalHours)*time.Hour, func(t time.Time) tea.Msg {
		return autoScanTickMsg(t)
	})
}

// listenForProgress returns a command that listens for progress updates from workers
func listenForProgress(wp *transcode.WorkerPool) tea.Cmd {
	return func() tea.Msg {
		// Wait for next progress update
		progressChan := wp.GetProgressChan()
		select {
		case update := <-progressChan:
			return progressMsg(update)
		case <-time.After(100 * time.Millisecond):
			return progressMsg(types.ProgressUpdate{})
		}
	}
}

// scanLibrary returns a command that scans the remote library
// If autoQueue is true, automatically queues jobs after scan completes
func scanLibrary(s *scanner.Scanner, db *database.DB, autoQueue bool) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()

		// Connect to remote server
		if err := s.Connect(ctx); err != nil {
			return errorMsg("Failed to connect to remote server: " + err.Error())
		}
		defer func() { _ = s.Close() }()

		// Create a channel for progress updates
		progressChan := make(chan scanner.ScanProgress, 100)

		// Start a goroutine to listen for progress and return as messages
		go func() {
			for progress := range progressChan {
				// We can't return messages from here, so we'll need a different approach
				_ = progress
			}
		}()

		// Scan library - the callback runs synchronously in the same goroutine
		// So we can't send messages from it. We need to poll instead.
		err := s.Scan(ctx, func(progress scanner.ScanProgress) {
			// Progress callback - runs in scan goroutine
			// Can't send tea.Msg from here
		})

		close(progressChan)

		if err != nil {
			return errorMsg("Scan failed: " + err.Error())
		}

		// Auto-queue jobs if requested
		autoQueued := 0
		if autoQueue {
			count, err := db.QueueJobsForTranscoding(0)
			if err == nil {
				autoQueued = count
			}
		}

		return scanCompleteMsg{autoQueued: autoQueued}
	}
}
