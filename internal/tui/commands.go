package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jbarbezat/transcoder/internal/database"
	"github.com/jbarbezat/transcoder/internal/scanner"
	"github.com/jbarbezat/transcoder/internal/transcode"
)

// Messages

type tickMsg time.Time
type progressMsg struct{}
type scanCompleteMsg struct{}
type errorMsg string

// Commands

// tickCmd returns a command that sends a tick message every second
func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// listenForProgress returns a command that listens for progress updates from workers
func listenForProgress(wp *transcode.WorkerPool) tea.Cmd {
	return func() tea.Msg {
		// Wait for next progress update
		progressChan := wp.GetProgressChan()
		select {
		case <-progressChan:
			return progressMsg{}
		case <-time.After(100 * time.Millisecond):
			return progressMsg{}
		}
	}
}

// scanLibrary returns a command that scans the remote library
func scanLibrary(s *scanner.Scanner, db *database.DB) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()

		// Connect to remote server
		if err := s.Connect(ctx); err != nil {
			return errorMsg("Failed to connect to remote server: " + err.Error())
		}
		defer s.Close()

		// Scan library
		err := s.Scan(ctx, func(progress scanner.ScanProgress) {
			// Progress updates will be handled by the model
		})

		if err != nil {
			return errorMsg("Scan failed: " + err.Error())
		}

		// Create jobs for files that should be transcoded
		// This would typically be done during the scan or in a separate step
		// For now, we just complete the scan

		return scanCompleteMsg{}
	}
}
