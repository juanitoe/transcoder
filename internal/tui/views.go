package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"transcoder/internal/types"
)

// renderHeader renders the top header bar
func (m Model) renderHeader() string {
	title := "📹 Video Transcoder TUI"

	// View tabs
	tabs := []string{
		"[1] Dashboard",
		"[2] Queue",
		"[3] History",
		"[4] Settings",
		"[5] Scanner",
		"[6] Logs",
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

	// Last update time
	updateText := statusStyle.Render(fmt.Sprintf("Updated: %s", m.lastUpdate.Format("15:04:05")))

	footer := lipgloss.JoinHorizontal(
		lipgloss.Top,
		statusText,
		strings.Repeat(" ", max(0, m.width-lipgloss.Width(statusText)-lipgloss.Width(helpText)-lipgloss.Width(updateText)-4)),
		helpText,
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

	// Statistics boxes
	statsBox := boxStyle.Render(fmt.Sprintf(
		"📊 Library Statistics\n\n"+
			"Total Files:      %d\n"+
			"To Transcode:     %d\n"+
			"In Progress:      %d\n"+
			"Completed:        %d\n"+
			"Failed:           %d\n\n"+
			"Total Size:       %s\n"+
			"Space Saved:      %s (%.1f%%)\n"+
			"Avg Reduction:    %.1f%%",
		stats.TotalFiles,
		stats.ToTranscode,
		stats.InProgress,
		stats.Completed,
		stats.Failed,
		formatBytes(stats.TotalSize),
		formatBytes(stats.SpaceSaved),
		stats.SpaceSavedPercent,
		stats.AvgSizeReduction,
	))

	// Worker status
	workerBox := boxStyle.Render(fmt.Sprintf(
		"⚙️  Worker Status\n\n"+
			"Active Workers:   %d\n"+
			"Max Workers:      %d\n"+
			"Jobs in Queue:    %d\n\n"+
			"Use [+/-] in Settings to scale workers",
		len(m.activeJobs),
		m.cfg.Workers.MaxWorkers,
		len(m.queuedJobs),
	))

	// Active jobs
	activeJobsStr := "🎬 Active Jobs\n\n"
	if len(m.activeJobs) == 0 {
		activeJobsStr += statusStyle.Render("No active jobs")
	} else {
		for _, job := range m.activeJobs {
			statusColor := lipgloss.NewStyle().Foreground(statusColor(job.Status))

			activeJobsStr += fmt.Sprintf(
				"%s  %s\n"+
				"    %s\n"+
				"    Stage: %s | %s\n\n",
				statusColor.Render(string(job.Status)),
				job.FileName,
				formatProgress(job.Progress),
				job.Stage,
				job.WorkerID,
			)
		}
	}
	activeBox := boxStyle.Render(activeJobsStr)

	// Layout: Two columns
	leftCol := lipgloss.JoinVertical(lipgloss.Left, statsBox, workerBox)
	rightCol := activeBox

	dashboard := lipgloss.JoinHorizontal(
		lipgloss.Top,
		leftCol,
		rightCol,
	)

	if m.showHelp {
		dashboard += "\n" + m.renderHelp()
	}

	return dashboard
}

// renderQueue renders the job queue view
func (m Model) renderQueue() string {
	content := "📋 Job Queue\n\n"

	if len(m.queuedJobs) == 0 {
		content += statusStyle.Render("No jobs in queue\n\n")
		content += "Press [s] to scan library and create jobs"
		return boxStyle.Render(content)
	}

	content += fmt.Sprintf("Total: %d jobs\n\n", len(m.queuedJobs))

	// Show jobs
	for i, job := range m.queuedJobs {
		style := lipgloss.NewStyle()
		if i == m.selectedJob {
			style = selectedStyle
		}

		prefix := "  "
		if i == m.selectedJob {
			prefix = "► "
		}

		statusColor := lipgloss.NewStyle().Foreground(statusColor(job.Status))

		content += style.Render(fmt.Sprintf(
			"%s[%d] %s  %s\n"+
			"     %s  •  Priority: %d\n",
			prefix,
			job.ID,
			statusColor.Render(string(job.Status)),
			job.FileName,
			formatBytes(job.FileSizeBytes),
			job.Priority,
		))
	}

	content += "\n" + helpStyle.Render("↑/↓ navigate  •  [p] pause  •  [c] cancel  •  [enter] resume")

	return boxStyle.Render(content)
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
			"Queue/History:\n" +
			"  [↑/↓] or j/k  Navigate jobs\n" +
			"  [p]           Pause job\n" +
			"  [c]           Cancel job\n" +
			"  [Enter]       Resume paused job\n\n" +
			"Settings:\n" +
			"  [+/-]         Scale worker count\n",
	)

	return help
}

// renderLogs renders the logs view with scrolling
func (m Model) renderLogs() string {
	content := "📝 Application Logs\n\n"

	if len(m.logs) == 0 {
		content += statusStyle.Render("No logs yet")
		return boxStyle.Render(content)
	}

	// Calculate how many logs we can display based on available height
	// Approximate: 2 lines for title, 2 for box borders, 3 for footer/header
	availableHeight := m.height - 7
	if availableHeight < 1 {
		availableHeight = 10 // Fallback minimum
	}

	// Calculate the window of logs to display based on scroll offset
	totalLogs := len(m.logs)
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

	// Display log entries
	for i := startIdx; i < endIdx; i++ {
		logEntry := m.logs[i]

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

		content += style.Render(logEntry) + "\n"
	}

	// Add scroll indicator
	if totalLogs > availableHeight {
		scrollInfo := fmt.Sprintf("\n%s (Showing %d-%d of %d logs)",
			helpStyle.Render("↑/↓ to scroll"),
			startIdx+1,
			endIdx,
			totalLogs,
		)
		content += scrollInfo
	}

	return boxStyle.Render(content)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
