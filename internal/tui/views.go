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
// renderJobs renders the jobs view with clickable panel tabs
func (m Model) renderJobs() string {
	visibleHeight := m.calculateVisibleJobsHeight()
	boxWidth := m.width - 4 // Full width minus margins

	// Create clickable tab-style panel switcher
	activeTabStyle := headerStyle
	queuedTabStyle := statusStyle

	if m.jobsPanel == 1 {
		activeTabStyle = statusStyle
		queuedTabStyle = headerStyle
	}

	panelSwitcher := lipgloss.JoinHorizontal(
		lipgloss.Left,
		activeTabStyle.Render(" 🎬 Active Jobs "),
		"  ",
		queuedTabStyle.Render(" 📋 Queued Jobs "),
	)

	// Adjust filename truncation based on box width
	fileNameWidth := boxWidth - 25 // Reserve space for prefix, job ID, etc.
	if fileNameWidth < 30 {
		fileNameWidth = 30
	}

	var panelContent string

	// Enhanced selected job style with background
	enhancedSelectedStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("230")).
		Background(lipgloss.Color("62")).
		Bold(true).
		Padding(0, 1)

	// Render the active panel
	if m.jobsPanel == 0 {
		// Active Jobs Panel
		if len(m.activeJobs) == 0 {
			panelContent = statusStyle.Render("No active jobs")
		} else {
			panelContent = fmt.Sprintf("Total: %d\n\n", len(m.activeJobs))

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
				prefix := "  "
				isSelected := (m.jobsPanel == 0 && i == m.selectedJob)

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

				jobText := fmt.Sprintf(
					"%sJob #%d  %s\n"+
						"      %s | %s%s\n"+
						"      %s\n",
					prefix,
					job.ID,
					truncateString(job.FileName, fileNameWidth),
					statusColor.Render(string(job.Status)),
					job.WorkerID,
					sizeInfo,
					formatProgress(job.Progress, job.Stage),
				)

				if isSelected {
					panelContent += enhancedSelectedStyle.Render("► "+jobText) + "\n"
				} else {
					panelContent += jobText + "\n"
				}
			}

			// Add scroll indicator if needed
			if len(m.activeJobs) > visibleHeight {
				panelContent += "\n" + helpStyle.Render(fmt.Sprintf("(Showing %d-%d of %d)", startIdx+1, endIdx, len(m.activeJobs)))
			}
		}
	} else {
		// Queued Jobs Panel
		if len(m.queuedJobs) == 0 {
			panelContent = statusStyle.Render("No queued jobs\n\n")
			panelContent += "Press [a] to queue jobs"
		} else {
			panelContent = fmt.Sprintf("Total: %d\n\n", len(m.queuedJobs))

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
				prefix := "  "
				isSelected := (m.jobsPanel == 1 && i == m.selectedJob)

				jobText := fmt.Sprintf(
					"%s[%d] %s\n"+
						"      %s  •  Pri: %d\n",
					prefix,
					job.ID,
					truncateString(job.FileName, fileNameWidth),
					formatBytes(job.FileSizeBytes),
					job.Priority,
				)

				if isSelected {
					panelContent += enhancedSelectedStyle.Render("► "+jobText) + "\n"
				} else {
					panelContent += jobText + "\n"
				}
			}

			// Add scroll indicator if needed
			if len(m.queuedJobs) > visibleHeight {
				panelContent += "\n" + helpStyle.Render(fmt.Sprintf("(Showing %d-%d of %d)", startIdx+1, endIdx, len(m.queuedJobs)))
			}
		}
	}

	// Create full-width box
	jobsBox := boxStyle.Copy().Width(boxWidth).Render(panelSwitcher + "\n\n" + panelContent)

	// Add help text
	if m.showJobActionDropdown {
		// Help text shown in dropdown
		helpText := ""
		return jobsBox + helpText
	} else {
		helpText := "\n" + helpStyle.Render("[Tab] switch panels  •  ↑/↓ navigate  •  [a] add  •  [Enter] Actions menu")
		return jobsBox + helpText
	}
}

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

	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	selectedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	editingStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)

	settingNames := []string{
		"Host:          ",
		"User:          ",
		"Port:          ",
		"SSH Key:       ",
		"Quality:       ",
		"Preset:        ",
		"Max Workers:   ",
		"Work Directory:",
		"Database Path: ",
	}

	// Remote Configuration
	content += "Remote Configuration:\n"
	for i := 0; i < 4; i++ {
		prefix := "  "
		if i == m.selectedSetting {
			if m.isEditingSettings {
				prefix = editingStyle.Render("▶ ")
			} else {
				prefix = selectedStyle.Render("▶ ")
			}
		} else {
			prefix = "  "
		}

		label := labelStyle.Render(settingNames[i])
		value := m.settingsInputs[i].Value()
		if i == m.selectedSetting && m.isEditingSettings {
			value = m.settingsInputs[i].View()
		}
		content += fmt.Sprintf("%s%s %s\n", prefix, label, value)
	}

	// Encoder Settings
	content += "\nEncoder Settings:\n"
	content += fmt.Sprintf("  %s %s (fixed)\n", labelStyle.Render("Codec:         "), m.cfg.Encoder.Codec)
	for i := 4; i < 6; i++ {
		prefix := "  "
		if i == m.selectedSetting {
			if m.isEditingSettings && !m.showPresetDropdown {
				prefix = editingStyle.Render("▶ ")
			} else {
				prefix = selectedStyle.Render("▶ ")
			}
		} else {
			prefix = "  "
		}

		label := labelStyle.Render(settingNames[i])
		value := m.settingsInputs[i].Value()
		if i == m.selectedSetting && m.isEditingSettings && !m.showPresetDropdown {
			value = m.settingsInputs[i].View()
		}
		content += fmt.Sprintf("%s%s %s\n", prefix, label, value)

		// Show dropdown after Preset field if active
		if i == 5 && m.showPresetDropdown {
			content += m.renderPresetDropdown()
		}
	}

	// Worker Configuration
	content += "\nWorker Configuration:\n"
	for i := 6; i < 8; i++ {
		prefix := "  "
		if i == m.selectedSetting {
			if m.isEditingSettings {
				prefix = editingStyle.Render("▶ ")
			} else {
				prefix = selectedStyle.Render("▶ ")
			}
		} else {
			prefix = "  "
		}

		label := labelStyle.Render(settingNames[i])
		value := m.settingsInputs[i].Value()
		if i == m.selectedSetting && m.isEditingSettings {
			value = m.settingsInputs[i].View()
		}
		content += fmt.Sprintf("%s%s %s\n", prefix, label, value)
	}

	// Database
	content += "\nDatabase:\n"
	i := 8
	prefix := "  "
	if i == m.selectedSetting {
		if m.isEditingSettings {
			prefix = editingStyle.Render("▶ ")
		} else {
			prefix = selectedStyle.Render("▶ ")
		}
	} else {
		prefix = "  "
	}
	label := labelStyle.Render(settingNames[i])
	value := m.settingsInputs[i].Value()
	if i == m.selectedSetting && m.isEditingSettings {
		value = m.settingsInputs[i].View()
	}
	content += fmt.Sprintf("%s%s %s\n", prefix, label, value)

	// Validation error
	if m.validationError != "" {
		content += "\n" + errorStyle.Render(fmt.Sprintf("⚠ %s", m.validationError))
	}

	// Help text
	content += "\n\n"
	if m.showSaveDiscardPrompt {
		// Show save/discard prompt
		content += m.renderSaveDiscardPrompt()
	} else if m.showPresetDropdown {
		// Help text is shown in the dropdown itself
	} else if m.isEditingSettings {
		content += helpStyle.Render("[Enter] Save • [Esc] Cancel")
	} else if m.configModified {
		content += helpStyle.Render("[↑↓] Navigate • [Enter] Edit • [s] Save/Discard changes")
	} else {
		content += helpStyle.Render("[↑↓] Navigate • [Enter] Edit")
	}

	if m.configModified && !m.showSaveDiscardPrompt {
		content += "\n" + successStyle.Render("⚠ Configuration modified (not saved to file)")
	}

	return boxStyle.Render(content)
}

// renderPresetDropdown renders a visual dropdown menu for encoder presets
func (m Model) renderPresetDropdown() string {
	presets := []string{"ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow"}
	descriptions := []string{
		"Fastest encoding, largest files",
		"Very fast, larger files",
		"Fast encoding, large files",
		"Faster encoding, balanced",
		"Fast, good balance",
		"Balanced speed/quality",
		"Slower, smaller files",
		"Very slow, much smaller",
		"Slowest, smallest files",
	}

	dropdownStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("62")).
		Padding(0, 1).
		MarginLeft(6)

	selectedItemStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("62")).
		Foreground(lipgloss.Color("230")).
		Bold(true)

	itemStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252"))

	descStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Italic(true)

	var dropdown string
	dropdown += "\n"

	for i, preset := range presets {
		var line string
		if i == m.presetDropdownIndex {
			line = selectedItemStyle.Render(fmt.Sprintf(" ▶ %-10s ", preset))
			line += " " + descStyle.Render(descriptions[i])
		} else {
			line = itemStyle.Render(fmt.Sprintf("   %-10s ", preset))
			line += " " + descStyle.Render(descriptions[i])
		}
		dropdown += line + "\n"
	}

	dropdown += "\n" + helpStyle.Render("  ↑↓ Navigate  •  Enter Select  •  Esc Cancel")

	return dropdownStyle.Render(dropdown) + "\n"
}

// renderSaveDiscardPrompt renders the save/discard choice prompt
func (m Model) renderSaveDiscardPrompt() string {
	promptStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("214")). // Orange/warning color
		Padding(0, 1)

	selectedStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("214")).
		Foreground(lipgloss.Color("0")).
		Bold(true)

	normalStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252"))

	var prompt string
	prompt += "⚠  Unsaved Changes\n\n"

	// Save option
	if m.saveDiscardPromptIndex == 0 {
		prompt += selectedStyle.Render(" ▶ Save to File     ") + "\n"
	} else {
		prompt += normalStyle.Render("   Save to File     ") + "\n"
	}

	// Discard option
	if m.saveDiscardPromptIndex == 1 {
		prompt += selectedStyle.Render(" ▶ Discard Changes ") + "\n"
	} else {
		prompt += normalStyle.Render("   Discard Changes ") + "\n"
	}

	prompt += "\n" + helpStyle.Render("↑↓/←→ Navigate  •  Enter Select  •  Esc Cancel")

	return promptStyle.Render(prompt)
}

// renderJobActionsDropdown renders a visual dropdown menu for job actions
func (m Model) renderJobActionsDropdown(job *types.TranscodeJob) string {
	actions := m.getJobActions(job)

	dropdownStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("62")).
		Padding(0, 1).
		MarginLeft(6)

	selectedItemStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("62")).
		Foreground(lipgloss.Color("230")).
		Bold(true)

	itemStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252"))

	descStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Italic(true)

	var dropdown string
	dropdown += "\n"

	for i, action := range actions {
		var line string
		if i == m.jobActionDropdownIndex {
			line = selectedItemStyle.Render(fmt.Sprintf(" ▶ %-16s ", action.label))
			line += " " + descStyle.Render(action.description)
		} else {
			line = itemStyle.Render(fmt.Sprintf("   %-16s ", action.label))
			line += " " + descStyle.Render(action.description)
		}
		dropdown += line + "\n"
	}

	dropdown += "\n" + helpStyle.Render("  ↑↓ Navigate  •  Enter Select  •  Esc Cancel")

	return dropdownStyle.Render(dropdown) + "\n"
}

// renderScanner renders the scanner view
func (m Model) renderScanner() string {
	content := "🔍 Library Scanner\n\n"

	if m.scanning {
		content += successStyle.Render("Scanning in progress...\n\n")

		// Calculate progress percentage
		progressStr := ""
		if m.scanProgress.TotalFiles > 0 {
			pct := float64(m.scanProgress.FilesScanned) / float64(m.scanProgress.TotalFiles) * 100
			progressStr = fmt.Sprintf(" (%.1f%%)", pct)
		}

		// Format scan rates
		filesRateStr := ""
		if m.scanProgress.FilesPerSec > 0 {
			filesRateStr = fmt.Sprintf(" • %.1f files/sec", m.scanProgress.FilesPerSec)
		}
		bytesRateStr := ""
		if m.scanProgress.BytesPerSec > 0 {
			bytesRateStr = fmt.Sprintf(" • %s/sec", formatBytes(int64(m.scanProgress.BytesPerSec)))
		}

		// Build files scanned line
		filesLine := fmt.Sprintf("Files Scanned:    %d", m.scanProgress.FilesScanned)
		if m.scanProgress.TotalFiles > 0 {
			filesLine += fmt.Sprintf("/%d%s%s", m.scanProgress.TotalFiles, progressStr, filesRateStr)
		} else if filesRateStr != "" {
			filesLine += filesRateStr
		}

		content += fmt.Sprintf(
			"Current Path:     %s\n"+
				"%s\n"+
				"Files Added:      %d\n"+
				"Files Updated:    %d\n"+
				"Bytes Scanned:    %s%s\n"+
				"Errors:           %d\n",
			m.scanProgress.CurrentPath,
			filesLine,
			m.scanProgress.FilesAdded,
			m.scanProgress.FilesUpdated,
			formatBytes(m.scanProgress.BytesScanned),
			bytesRateStr,
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
			"  [s]           Start library scan (except in Settings)\n" +
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
			"  [↑/↓]         Navigate settings\n" +
			"  [Enter]       Edit selected setting\n" +
			"  [s]           Save/discard changes\n" +
			"  [Ctrl+S]      Quick save (no prompt)\n" +
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
