package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"transcoder/internal/types"
	"transcoder/internal/version"
)

const (
	boxPadding = 8 // Consistent padding for all boxes (borders + padding + margins)
)

// renderHeader renders the top header bar
func (m Model) renderHeader() string {
	title := "📹 Video Transcoder TUI"

	// View tabs
	tabs := []string{
		"[1] Dashboard",
		"[2] Jobs",
		"[3] Settings",
		"[4] Scanner",
		"[5] Logs",
	}

	// Highlight active tab
	tabStrs := make([]string, len(tabs))
	for i, tab := range tabs {
		if ViewMode(i) == m.viewMode {
			tabStrs[i] = headerStyle.Render(tab)
		} else {
			tabStrs[i] = statusStyle.Render(tab)
		}
	}

	tabBar := strings.Join(tabStrs, "  ")

	// Create header layout
	header := lipgloss.JoinHorizontal(
		lipgloss.Top,
		titleStyle.Render(title),
		strings.Repeat(" ", max(0, m.width-lipgloss.Width(title)-lipgloss.Width(tabBar)-2)),
		tabBar,
	)

	return header
}

// renderFooter renders the bottom status bar
func (m Model) renderFooter() string {
	// Status/error message
	var statusText string
	if m.errorMsg != "" {
		statusText = errorStyle.Render("ERROR: " + m.errorMsg)
	} else if m.statusMsg != "" {
		statusText = successStyle.Render(m.statusMsg)
	} else {
		statusText = statusStyle.Render("Ready")
	}

	// Help hints
	helpText := helpStyle.Render("Press [h] for help  •  [q] quit  •  [r] refresh")

	// Version and last update time
	versionText := statusStyle.Render(fmt.Sprintf("v%s", version.GetVersion()))
	updateText := statusStyle.Render(fmt.Sprintf("Updated: %s", m.lastUpdate.Format("15:04:05")))

	footer := lipgloss.JoinHorizontal(
		lipgloss.Top,
		statusText,
		strings.Repeat(" ", max(0, m.width-lipgloss.Width(statusText)-lipgloss.Width(helpText)-lipgloss.Width(versionText)-lipgloss.Width(updateText)-6)),
		helpText,
		"  ",
		versionText,
		"  ",
		updateText,
	)

	return footer
}

// renderDashboard renders the main dashboard view
func (m Model) renderDashboard() string {
	if m.statistics == nil {
		return "Loading statistics..."
	}

	stats := m.statistics

	// Full width boxes with consistent padding
	boxWidth := m.width - 4 // Leave small margin
	if boxWidth < 60 {
		boxWidth = 60 // Minimum width
	}

	// Library Statistics - Full width with formatted labels
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")) // Dim gray for labels

	statsContent := fmt.Sprintf(
		"📊 Library Statistics\n\n"+
			"%s %d     %s %d     %s %d     %s %d     %s %d\n"+
			"%s %s     %s %s (%.1f%%)     %s %.1f%%",
		labelStyle.Render("Total Files:"),
		stats.TotalFiles,
		labelStyle.Render("To Transcode:"),
		stats.ToTranscode,
		labelStyle.Render("In Progress:"),
		stats.InProgress,
		labelStyle.Render("Completed:"),
		stats.Completed,
		labelStyle.Render("Failed:"),
		stats.Failed,
		labelStyle.Render("Total Size:"),
		formatBytes(stats.TotalSize),
		labelStyle.Render("Space Saved:"),
		formatBytes(stats.SpaceSaved),
		stats.SpaceSavedPercent,
		labelStyle.Render("Avg Reduction:"),
		stats.AvgSizeReduction,
	)

	statsBox := boxStyle.Copy().
		Width(boxWidth).
		Render(statsContent)

	// Worker Status - Full width with formatted labels
	workerContent := fmt.Sprintf(
		"⚙️  Worker Status     •     %s %d     •     %s %d     •     %s %d     •     %s",
		labelStyle.Render("Active:"),
		len(m.activeJobs),
		labelStyle.Render("Max:"),
		m.cfg.Workers.MaxWorkers,
		labelStyle.Render("Queued Jobs:"),
		len(m.queuedJobs),
		labelStyle.Render("Use [+/-] in Settings to scale workers"),
	)

	workerBox := boxStyle.Copy().
		Width(boxWidth).
		Render(workerContent)

	// Active Jobs - Full width
	activeJobsStr := "🎬 Active Jobs\n\n"
	if len(m.activeJobs) == 0 {
		activeJobsStr += statusStyle.Render("No active jobs")
	} else {
		for _, job := range m.activeJobs {
			statusColor := lipgloss.NewStyle().Foreground(statusColor(job.Status))

			// Show file size info based on job status
			sizeInfo := ""
			if job.Status == types.StatusDownloading || job.Status == types.StatusUploading {
				// Show bytes transferred for download/upload with bandwidth
				if history, ok := m.progressData[job.ID]; ok {
					if progress := history.Latest(); progress != nil && progress.TotalBytes > 0 {
						bandwidth := history.CalculateBandwidth()
						if bandwidth > 0 {
							sizeInfo = fmt.Sprintf(" • %s / %s • %s/s",
								formatBytes(progress.BytesTransferred),
								formatBytes(progress.TotalBytes),
								formatBytes(int64(bandwidth)))
						} else {
							sizeInfo = fmt.Sprintf(" • %s / %s",
								formatBytes(progress.BytesTransferred),
								formatBytes(progress.TotalBytes))
						}
					}
				}
			} else if job.Status == types.StatusTranscoding && job.TranscodedFileSizeBytes > 0 {
				sizeInfo = fmt.Sprintf(" • %s / %s",
					formatBytes(job.TranscodedFileSizeBytes),
					formatBytes(job.FileSizeBytes))
			}

			activeJobsStr += fmt.Sprintf(
				"Job #%d  %s\n"+
					"    %s | %s%s\n"+
					"    %s\n\n",
				job.ID,
				truncateString(job.FileName, 60),
				statusColor.Render(string(job.Status)),
				job.WorkerID,
				sizeInfo,
				formatProgress(job.Progress, job.Stage),
			)
		}
	}
	activeBox := boxStyle.Copy().
		Width(boxWidth).
		Render(activeJobsStr)

	// Stack vertically: Stats, Worker Status, Active Jobs
	dashboard := lipgloss.JoinVertical(
		lipgloss.Left,
		statsBox,
		workerBox,
		activeBox,
	)

	if m.showHelp {
		dashboard += "\n" + m.renderHelp()
	}

	return dashboard
}

// renderJobs renders the jobs view with two panels: active and queued
func (m Model) renderJobs() string {
	visibleHeight := m.calculateVisibleJobsHeight()

	// Calculate panel dimensions based on terminal size
	// Use fallback if dimensions not set yet
	termWidth := m.width
	termHeight := m.height
	if termWidth <= 0 {
		termWidth = 120 // Fallback default
	}
	if termHeight <= 0 {
		termHeight = 40 // Fallback default
	}

	// Each panel gets half the width minus borders and padding
	panelWidth := (termWidth / 2) - boxPadding
	if panelWidth < 40 {
		panelWidth = 40 // Minimum width
	}

	// Calculate max panel height: terminal height - header - footer - help text - margins
	// Header (~3 lines), Footer (~2 lines), Help text (~2 lines), margins (~3 lines)
	panelHeight := termHeight - 10
	if panelHeight < 15 {
		panelHeight = 15 // Minimum height
	}

	// Adjust filename truncation based on panel width
	fileNameWidth := panelWidth - 20 // Reserve space for prefix, job ID, etc.
	if fileNameWidth < 20 {
		fileNameWidth = 20
	}

	// Active Jobs Panel
	activeTitle := "🎬 Active Jobs"
	if m.jobsPanel == 0 {
		activeTitle = headerStyle.Render("🎬 Active Jobs [SELECTED]")
	}

	activeContent := activeTitle + "\n\n"
	if len(m.activeJobs) == 0 {
		activeContent += statusStyle.Render("No active jobs")
	} else {
		activeContent += fmt.Sprintf("Total: %d\n\n", len(m.activeJobs))

		// Calculate visible window
		startIdx := m.activeJobsScrollOffset
		endIdx := startIdx + visibleHeight
		if endIdx > len(m.activeJobs) {
			endIdx = len(m.activeJobs)
		}
		if startIdx >= len(m.activeJobs) {
			startIdx = max(0, len(m.activeJobs)-visibleHeight)
		}

		// Render visible jobs
		for i := startIdx; i < endIdx; i++ {
			job := m.activeJobs[i]
			style := lipgloss.NewStyle()
			prefix := "  "
			if m.jobsPanel == 0 && i == m.selectedJob {
				style = selectedStyle
				prefix = "► "
			}

			statusColor := lipgloss.NewStyle().Foreground(statusColor(job.Status))

			// Show file size info based on job status
			sizeInfo := ""
			if job.Status == types.StatusDownloading || job.Status == types.StatusUploading {
				// Show bytes transferred for download/upload with bandwidth
				if history, ok := m.progressData[job.ID]; ok {
					if progress := history.Latest(); progress != nil && progress.TotalBytes > 0 {
						bandwidth := history.CalculateBandwidth()
						if bandwidth > 0 {
							sizeInfo = fmt.Sprintf("  (%s / %s • %s/s)",
								formatBytes(progress.BytesTransferred),
								formatBytes(progress.TotalBytes),
								formatBytes(int64(bandwidth)))
						} else {
							sizeInfo = fmt.Sprintf("  (%s / %s)",
								formatBytes(progress.BytesTransferred),
								formatBytes(progress.TotalBytes))
						}
					}
				}
			} else if job.Status == types.StatusTranscoding && job.TranscodedFileSizeBytes > 0 {
				sizeInfo = fmt.Sprintf("  (%s / %s)",
					formatBytes(job.TranscodedFileSizeBytes),
					formatBytes(job.FileSizeBytes))
			}

			activeContent += style.Render(fmt.Sprintf(
				"%sJob #%d  %s\n"+
				"      %s | %s%s\n"+
				"      %s\n\n",
				prefix,
				job.ID,
				truncateString(job.FileName, fileNameWidth),
				statusColor.Render(string(job.Status)),
				job.WorkerID,
				sizeInfo,
				formatProgress(job.Progress, job.Stage),
			))
		}

		// Add scroll indicator if needed
		if len(m.activeJobs) > visibleHeight {
			activeContent += fmt.Sprintf("\n%s", helpStyle.Render(fmt.Sprintf("(Showing %d-%d of %d)", startIdx+1, endIdx, len(m.activeJobs))))
		}
	}

	// Create styled panel with width and height constraints
	activePanelStyle := boxStyle.Copy().Width(panelWidth).MaxWidth(panelWidth).Height(panelHeight).MaxHeight(panelHeight)
	activePanel := activePanelStyle.Render(activeContent)

	// Queued Jobs Panel
	queuedTitle := "📋 Queued Jobs"
	if m.jobsPanel == 1 {
		queuedTitle = headerStyle.Render("📋 Queued Jobs [SELECTED]")
	}

	queuedContent := queuedTitle + "\n\n"
	if len(m.queuedJobs) == 0 {
		queuedContent += statusStyle.Render("No queued jobs\n\n")
		queuedContent += "Press [a] to queue jobs"
	} else {
		queuedContent += fmt.Sprintf("Total: %d\n\n", len(m.queuedJobs))

		// Calculate visible window
		startIdx := m.queuedJobsScrollOffset
		endIdx := startIdx + visibleHeight
		if endIdx > len(m.queuedJobs) {
			endIdx = len(m.queuedJobs)
		}
		if startIdx >= len(m.queuedJobs) {
			startIdx = max(0, len(m.queuedJobs)-visibleHeight)
		}

		// Render visible jobs
		for i := startIdx; i < endIdx; i++ {
			job := m.queuedJobs[i]
			style := lipgloss.NewStyle()
			prefix := "  "
			if m.jobsPanel == 1 && i == m.selectedJob {
				style = selectedStyle
				prefix = "► "
			}

			queuedContent += style.Render(fmt.Sprintf(
				"%s[%d] %s\n"+
				"      %s  •  Pri: %d\n\n",
				prefix,
				job.ID,
				truncateString(job.FileName, fileNameWidth),
				formatBytes(job.FileSizeBytes),
				job.Priority,
			))
		}

		// Add scroll indicator if needed
		if len(m.queuedJobs) > visibleHeight {
			queuedContent += fmt.Sprintf("\n%s", helpStyle.Render(fmt.Sprintf("(Showing %d-%d of %d)", startIdx+1, endIdx, len(m.queuedJobs))))
		}
	}

	// Create styled panel with width and height constraints
	queuedPanelStyle := boxStyle.Copy().Width(panelWidth).MaxWidth(panelWidth).Height(panelHeight).MaxHeight(panelHeight)
	queuedPanel := queuedPanelStyle.Render(queuedContent)

	// Layout panels side by side
	layout := lipgloss.JoinHorizontal(lipgloss.Top, activePanel, queuedPanel)

	// Add help text
	helpText := "\n" + helpStyle.Render("[Tab] switch panels  •  ↑/↓ navigate  •  [a] add  •  [d] delete  •  [K] kill  •  [p] pause  •  [c] cancel  •  [enter] resume")

	return layout + helpText
}

// renderHistory renders the job history view
func (m Model) renderHistory() string {
	content := "📜 Job History\n\n"

	if len(m.recentJobs) == 0 {
		content += statusStyle.Render("No completed jobs")
		return boxStyle.Render(content)
	}

	// Show recent jobs
	for i, job := range m.recentJobs {
		style := lipgloss.NewStyle()
		if i == m.selectedJob {
			style = selectedStyle
		}

		prefix := "  "
		if i == m.selectedJob {
			prefix = "► "
		}

		statusColor := lipgloss.NewStyle().Foreground(statusColor(job.Status))

		var details string
		if job.Status == types.StatusCompleted {
			originalSize := job.FileSizeBytes
			newSize := job.TranscodedFileSizeBytes
			if newSize > 0 && originalSize > 0 {
				saved := originalSize - newSize
				percent := (float64(saved) / float64(originalSize)) * 100
				details = fmt.Sprintf("Saved: %s (%.1f%%)  •  Time: %s",
					formatBytes(saved),
					percent,
					formatDuration(job.EncodingTimeSeconds),
				)
			}
		} else if job.Status == types.StatusFailed {
			details = fmt.Sprintf("Error: %s", job.ErrorMessage)
		}

		content += style.Render(fmt.Sprintf(
			"%s[%d] %s  %s\n"+
			"     %s\n",
			prefix,
			job.ID,
			statusColor.Render(string(job.Status)),
			job.FileName,
			details,
		))
	}

	return boxStyle.Render(content)
}

// renderSettings renders the settings view
func (m Model) renderSettings() string {
	content := "⚙️  Settings\n\n"

	content += fmt.Sprintf(
		"Remote Configuration:\n"+
			"  Host:           %s:%d\n"+
			"  User:           %s\n"+
			"  SSH Key:        %s\n"+
			"  Media Paths:    %d configured\n\n"+
			"Encoder Settings:\n"+
			"  Codec:          %s\n"+
			"  Quality:        %d/100\n"+
			"  Preset:         %s\n\n"+
			"Worker Configuration:\n"+
			"  Max Workers:    %d  [+/- to adjust]\n"+
			"  Work Directory: %s\n\n"+
			"Database:\n"+
			"  Path:           %s\n",
		m.cfg.Remote.Host,
		m.cfg.Remote.Port,
		m.cfg.Remote.User,
		m.cfg.Remote.SSHKey,
		len(m.cfg.Remote.MediaPaths),
		m.cfg.Encoder.Codec,
		m.cfg.Encoder.Quality,
		m.cfg.Encoder.Preset,
		m.cfg.Workers.MaxWorkers,
		m.cfg.Workers.WorkDir,
		m.cfg.Database.Path,
	)

	content += "\n" + helpStyle.Render("Use [+/-] to adjust worker count")

	return boxStyle.Render(content)
}

// renderScanner renders the scanner view
func (m Model) renderScanner() string {
	content := "🔍 Library Scanner\n\n"

	if m.scanning {
		content += successStyle.Render("Scanning in progress...\n\n")
		content += fmt.Sprintf(
			"Current Path:     %s\n"+
				"Files Scanned:    %d\n"+
				"Files Added:      %d\n"+
				"Files Updated:    %d\n"+
				"Bytes Scanned:    %s\n"+
				"Errors:           %d\n",
			m.scanProgress.CurrentPath,
			m.scanProgress.FilesScanned,
			m.scanProgress.FilesAdded,
			m.scanProgress.FilesUpdated,
			formatBytes(m.scanProgress.BytesScanned),
			m.scanProgress.ErrorCount,
		)

		if m.scanProgress.LastError != nil {
			content += "\n" + errorStyle.Render(fmt.Sprintf("Last Error: %v", m.scanProgress.LastError))
		}
	} else {
		content += "Press [s] to start scanning remote library\n\n"

		content += "Configured media paths:\n"
		for _, path := range m.cfg.Remote.MediaPaths {
			content += fmt.Sprintf("  • %s\n", path)
		}
	}

	return boxStyle.Render(content)
}

// renderHelp renders the help overlay
func (m Model) renderHelp() string {
	help := boxStyle.Render(
		"⌨️  Keyboard Shortcuts\n\n" +
			"Global:\n" +
			"  [1-6]         Switch views\n" +
			"  [h/?]         Toggle help\n" +
			"  [r]           Refresh data\n" +
			"  [s]           Start library scan\n" +
			"  [q/Ctrl+C]    Quit\n\n" +
			"Jobs View:\n" +
			"  [↑/↓] or j/k  Navigate jobs\n" +
			"  [a]           Add/queue jobs for transcoding\n" +
			"  [d]           Delete job (terminal states only)\n" +
			"  [K]           Kill job (force cancel)\n" +
			"  [p]           Pause job\n" +
			"  [c]           Cancel job\n" +
			"  [Enter]       Resume paused job\n\n" +
			"Settings:\n" +
			"  [+/-]         Scale worker count\n",
	)

	return help
}

// renderLogs renders the logs view with scrolling (newest first)
func (m Model) renderLogs() string {
	title := "📝 Application Logs\n\n"

	// Ensure we have valid dimensions
	width := m.width
	if width <= 0 {
		width = 80 // Fallback default
	}
	height := m.height
	if height <= 0 {
		height = 24 // Fallback default
	}

	if len(m.logs) == 0 {
		content := title + statusStyle.Render("No logs yet")
		return boxStyle.Width(width - 4).Render(content)
	}

	// Calculate how many logs we can display based on available height
	// Account for: header (3), footer (3), title (2), box padding/borders (6), scroll indicator (2)
	availableHeight := height - 16
	if availableHeight < 5 {
		availableHeight = 5 // Fallback minimum
	}

	// Calculate the window of logs to display based on scroll offset
	// Display logs in reverse order (newest first)
	totalLogs := len(m.logs)

	// startIdx is from the end (newest), counting backwards
	startIdx := m.logScrollOffset
	endIdx := startIdx + availableHeight

	// Bounds checking
	if startIdx < 0 {
		startIdx = 0
	}
	if endIdx > totalLogs {
		endIdx = totalLogs
	}
	if startIdx >= totalLogs {
		startIdx = max(0, totalLogs-availableHeight)
	}

	// Build log content
	var logLines []string
	maxLogWidth := width - 12 // Account for box borders and padding
	if maxLogWidth < 20 {
		maxLogWidth = 20 // Minimum width
	}

	for i := endIdx - 1; i >= startIdx; i-- {
		logEntry := m.logs[totalLogs-1-i]

		// Truncate long log lines to fit width
		if len(logEntry) > maxLogWidth {
			logEntry = logEntry[:maxLogWidth-3] + "..."
		}

		// Color code based on log level
		var style lipgloss.Style
		if strings.Contains(logEntry, "ERROR") {
			style = errorStyle
		} else if strings.Contains(logEntry, "WARN") {
			style = lipgloss.NewStyle().Foreground(lipgloss.Color("208")) // Orange
		} else if strings.Contains(logEntry, "INFO") {
			style = lipgloss.NewStyle().Foreground(lipgloss.Color("42")) // Green
		} else {
			style = statusStyle
		}

		logLines = append(logLines, style.Render(logEntry))
	}

	content := title + strings.Join(logLines, "\n")

	// Add scroll indicator
	if totalLogs > availableHeight {
		scrollInfo := fmt.Sprintf("\n\n%s (Showing %d-%d of %d logs - newest first)",
			helpStyle.Render("↑/↓ to scroll"),
			startIdx+1,
			endIdx,
			totalLogs,
		)
		content += scrollInfo
	}

	return boxStyle.
		Width(width - 4).
		MaxHeight(height - 8).
		Render(content)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
