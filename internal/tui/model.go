package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"transcoder/internal/config"
	"transcoder/internal/database"
	"transcoder/internal/scanner"
	"transcoder/internal/transcode"
	"transcoder/internal/types"
)

// ViewMode represents different TUI screens
type ViewMode int

const (
	ViewDashboard ViewMode = iota
	ViewQueue
	ViewHistory
	ViewSettings
	ViewScanner
	ViewLogs
)

// Model is the Bubble Tea model for the TUI
type Model struct {
	cfg          *config.Config
	db           *database.DB
	scanner      *scanner.Scanner
	workerPool   *transcode.WorkerPool

	// State
	viewMode     ViewMode
	width        int
	height       int
	lastUpdate   time.Time

	// Data
	statistics   *types.Statistics
	activeJobs   []*types.TranscodeJob
	queuedJobs   []*types.TranscodeJob
	recentJobs   []*types.TranscodeJob

	// Scanner state
	scanning     bool
	scanProgress scanner.ScanProgress

	// UI State
	selectedJob    int
	selectedSetting int
	statusMsg      string
	errorMsg       string

	// Keyboard hints
	showHelp       bool
	configModified bool

	// Logs
	logs            []string
	maxLogs         int
	logScrollOffset int
	scannerLogPath  string
	scannerLogPos   int64
}

// New creates a new TUI model
func New(cfg *config.Config, db *database.DB, scanner *scanner.Scanner, workerPool *transcode.WorkerPool) Model {
	// Expand scanner log path
	scannerLogPath := os.ExpandEnv(cfg.Logging.ScannerLog)
	if strings.HasPrefix(scannerLogPath, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			scannerLogPath = strings.Replace(scannerLogPath, "~", home, 1)
		}
	}

	m := Model{
		cfg:            cfg,
		db:             db,
		scanner:        scanner,
		workerPool:     workerPool,
		viewMode:       ViewDashboard,
		lastUpdate:     time.Now(),
		logs:           make([]string, 0),
		maxLogs:        200,
		scannerLogPath: scannerLogPath,
		scannerLogPos:  0,
	}
	m.addLog("INFO", "Transcoder TUI started")
	return m
}

// Init initializes the model
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		tea.EnterAltScreen,
		tickCmd(),
		listenForProgress(m.workerPool),
	)
}

// Update handles messages
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.handleKeyPress(msg)

	case tickMsg:
		// Periodic data refresh
		m.refreshData()

		// Poll scanner progress if scanning
		if m.scanning {
			progress := m.scanner.GetProgress()
			if progress.FilesScanned != m.scanProgress.FilesScanned {
				m.scanProgress = progress
				m.statusMsg = fmt.Sprintf("Scanning: %d files, %d added, %d updated",
					progress.FilesScanned, progress.FilesAdded, progress.FilesUpdated)
				if progress.LastError != nil {
					m.addLog("ERROR", fmt.Sprintf("Scan error: %v", progress.LastError))
				}
			}
		}

		// Tail scanner log file
		m.tailScannerLog()

		return m, tickCmd()

	case progressMsg:
		// Handle progress update from workers
		m.refreshData()
		return m, listenForProgress(m.workerPool)

	case scanProgressMsg:
		// Handle scan progress update
		m.scanProgress = scanner.ScanProgress(msg)
		m.statusMsg = fmt.Sprintf("Scanning: %d files, %d added, %d updated",
			m.scanProgress.FilesScanned, m.scanProgress.FilesAdded, m.scanProgress.FilesUpdated)
		if m.scanProgress.LastError != nil {
			m.errorMsg = fmt.Sprintf("Scan error: %v", m.scanProgress.LastError)
			m.addLog("ERROR", fmt.Sprintf("Scan error: %v", m.scanProgress.LastError))
		}
		return m, nil

	case scanCompleteMsg:
		m.scanning = false
		m.statusMsg = fmt.Sprintf("Scan complete: %d files scanned, %d added, %d updated",
			m.scanProgress.FilesScanned, m.scanProgress.FilesAdded, m.scanProgress.FilesUpdated)
		m.addLog("INFO", fmt.Sprintf("Scan complete: %d files scanned, %d added, %d updated",
			m.scanProgress.FilesScanned, m.scanProgress.FilesAdded, m.scanProgress.FilesUpdated))
		m.refreshData()
		return m, nil

	case errorMsg:
		m.errorMsg = string(msg)
		m.addLog("ERROR", string(msg))
		m.scanning = false // Stop scanning on error
		return m, nil
	}

	return m, nil
}

// View renders the TUI
func (m Model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	var content string

	switch m.viewMode {
	case ViewDashboard:
		content = m.renderDashboard()
	case ViewQueue:
		content = m.renderQueue()
	case ViewHistory:
		content = m.renderHistory()
	case ViewSettings:
		content = m.renderSettings()
	case ViewScanner:
		content = m.renderScanner()
	case ViewLogs:
		content = m.renderLogs()
	default:
		content = m.renderDashboard()
	}

	// Add header and footer
	header := m.renderHeader()
	footer := m.renderFooter()

	// Calculate content height
	contentHeight := m.height - lipgloss.Height(header) - lipgloss.Height(footer) - 2

	// Ensure content fits
	contentStyle := lipgloss.NewStyle().
		Width(m.width).
		Height(contentHeight)

	return lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		contentStyle.Render(content),
		footer,
	)
}

// handleKeyPress processes keyboard input
func (m Model) handleKeyPress(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Global keys
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit

	case "h", "?":
		m.showHelp = !m.showHelp
		return m, nil

	case "1":
		m.viewMode = ViewDashboard
		m.refreshData()
		return m, nil

	case "2":
		m.viewMode = ViewQueue
		m.refreshData()
		return m, nil

	case "3":
		m.viewMode = ViewHistory
		m.refreshData()
		return m, nil

	case "4":
		m.viewMode = ViewSettings
		return m, nil

	case "5":
		m.viewMode = ViewScanner
		return m, nil

	case "6":
		m.viewMode = ViewLogs
		return m, nil

	case "s":
		// Start scan
		if !m.scanning {
			m.scanning = true
			m.statusMsg = "Starting scan..."
			m.errorMsg = "" // Clear any previous errors
			m.addLog("INFO", "Starting library scan")
			return m, scanLibrary(m.scanner, m.db)
		}
		return m, nil

	case "r":
		// Refresh data
		m.refreshData()
		m.statusMsg = "Data refreshed"
		return m, nil
	}

	// View-specific keys
	switch m.viewMode {
	case ViewQueue, ViewHistory:
		return m.handleJobListKeys(msg)
	case ViewSettings:
		return m.handleSettingsKeys(msg)
	case ViewLogs:
		return m.handleLogsKeys(msg)
	}

	return m, nil
}

// handleJobListKeys handles keys in job list views
func (m Model) handleJobListKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	jobs := m.queuedJobs
	if m.viewMode == ViewHistory {
		jobs = m.recentJobs
	}

	switch msg.String() {
	case "up", "k":
		if m.selectedJob > 0 {
			m.selectedJob--
		}

	case "down", "j":
		if m.selectedJob < len(jobs)-1 {
			m.selectedJob++
		}

	case "p":
		// Pause selected job
		if m.selectedJob < len(jobs) {
			job := jobs[m.selectedJob]
			if job.Status == types.StatusTranscoding || job.Status == types.StatusDownloading {
				m.workerPool.PauseJob(job.ID)
				m.statusMsg = fmt.Sprintf("Pausing job #%d", job.ID)
			}
		}

	case "c":
		// Cancel selected job
		if m.selectedJob < len(jobs) {
			job := jobs[m.selectedJob]
			m.workerPool.CancelJob(job.ID)
			m.statusMsg = fmt.Sprintf("Canceling job #%d", job.ID)
		}

	case "enter":
		// Resume paused job
		if m.selectedJob < len(jobs) {
			job := jobs[m.selectedJob]
			if job.Status == types.StatusPaused {
				m.db.ResumeJob(job.ID)
				m.statusMsg = fmt.Sprintf("Resuming job #%d", job.ID)
			}
		}
	}

	return m, nil
}

// handleSettingsKeys handles keys in settings view
func (m Model) handleSettingsKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.selectedSetting > 0 {
			m.selectedSetting--
		}

	case "down", "j":
		if m.selectedSetting < 2 { // 3 editable settings (0-2)
			m.selectedSetting++
		}

	case "left", "h", "-":
		m.configModified = true
		switch m.selectedSetting {
		case 0: // Max Workers
			if m.cfg.Workers.MaxWorkers > 1 {
				m.workerPool.ScaleWorkers(m.cfg.Workers.MaxWorkers - 1)
				m.statusMsg = fmt.Sprintf("Workers: %d", m.cfg.Workers.MaxWorkers)
			}
		case 1: // Quality
			if m.cfg.Encoder.Quality > 10 {
				m.cfg.Encoder.Quality -= 5
				m.statusMsg = fmt.Sprintf("Quality: %d", m.cfg.Encoder.Quality)
			}
		case 2: // Preset cycle
			presets := []string{"ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow"}
			for i, p := range presets {
				if m.cfg.Encoder.Preset == p && i > 0 {
					m.cfg.Encoder.Preset = presets[i-1]
					m.statusMsg = fmt.Sprintf("Preset: %s", m.cfg.Encoder.Preset)
					break
				}
			}
		}

	case "right", "l", "+", "=":
		m.configModified = true
		switch m.selectedSetting {
		case 0: // Max Workers
			m.workerPool.ScaleWorkers(m.cfg.Workers.MaxWorkers + 1)
			m.statusMsg = fmt.Sprintf("Workers: %d", m.cfg.Workers.MaxWorkers)
		case 1: // Quality
			if m.cfg.Encoder.Quality < 100 {
				m.cfg.Encoder.Quality += 5
				m.statusMsg = fmt.Sprintf("Quality: %d", m.cfg.Encoder.Quality)
			}
		case 2: // Preset cycle
			presets := []string{"ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow"}
			for i, p := range presets {
				if m.cfg.Encoder.Preset == p && i < len(presets)-1 {
					m.cfg.Encoder.Preset = presets[i+1]
					m.statusMsg = fmt.Sprintf("Preset: %s", m.cfg.Encoder.Preset)
					break
				}
			}
		}

	case "w":
		// Save config
		configPath := os.ExpandEnv("$HOME/transcoder/config.yaml")
		if err := m.cfg.Save(configPath); err != nil {
			m.errorMsg = fmt.Sprintf("Failed to save config: %v", err)
		} else {
			m.statusMsg = "Configuration saved!"
			m.configModified = false
		}
	}

	return m, nil
}

// handleLogsKeys handles keys in logs view
func (m Model) handleLogsKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Calculate available height for logs
	availableHeight := m.height - 7
	if availableHeight < 1 {
		availableHeight = 10
	}

	maxScrollOffset := len(m.logs) - availableHeight
	if maxScrollOffset < 0 {
		maxScrollOffset = 0
	}

	switch msg.String() {
	case "up", "k":
		if m.logScrollOffset > 0 {
			m.logScrollOffset--
		}

	case "down", "j":
		if m.logScrollOffset < maxScrollOffset {
			m.logScrollOffset++
		}

	case "home", "g":
		// Jump to top
		m.logScrollOffset = 0

	case "end", "G":
		// Jump to bottom
		m.logScrollOffset = maxScrollOffset

	case "pageup":
		// Scroll up one page
		m.logScrollOffset -= availableHeight
		if m.logScrollOffset < 0 {
			m.logScrollOffset = 0
		}

	case "pagedown":
		// Scroll down one page
		m.logScrollOffset += availableHeight
		if m.logScrollOffset > maxScrollOffset {
			m.logScrollOffset = maxScrollOffset
		}
	}

	return m, nil
}

// refreshData updates all data from the database
func (m *Model) refreshData() {
	var err error

	// Get statistics
	m.statistics, err = m.db.GetStatistics()
	if err != nil {
		m.errorMsg = fmt.Sprintf("Failed to get statistics: %v", err)
	}

	// Get active jobs
	m.activeJobs, err = m.db.GetActiveJobs()
	if err != nil {
		m.errorMsg = fmt.Sprintf("Failed to get active jobs: %v", err)
	}

	// Get queued jobs
	m.queuedJobs, err = m.db.GetQueuedJobs(100)
	if err != nil {
		m.errorMsg = fmt.Sprintf("Failed to get queued jobs: %v", err)
	}

	// Get recent jobs (limited)
	// Note: We'll need to add a GetRecentJobs method to the database
	// For now, combine active and some completed jobs
	m.recentJobs = m.activeJobs

	m.lastUpdate = time.Now()
}

// Styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205")).
			MarginBottom(1)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Background(lipgloss.Color("62")).
			Foreground(lipgloss.Color("230")).
			Padding(0, 1)

	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196")).
			Bold(true)

	successStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("42")).
			Bold(true)

	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("62")).
			Padding(1, 2).
			MarginBottom(1)

	selectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("205")).
			Bold(true)

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")).
			Italic(true)
)

// Helper functions for formatting
func formatBytes(bytes int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
		TB = 1024 * GB
	)

	switch {
	case bytes >= TB:
		return fmt.Sprintf("%.2f TB", float64(bytes)/float64(TB))
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

func formatDuration(seconds int) string {
	d := time.Duration(seconds) * time.Second
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60

	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	} else if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

func formatProgress(progress float64) string {
	bar := progressBar(progress, 20)
	return fmt.Sprintf("[%s] %.1f%%", bar, progress)
}

func progressBar(progress float64, width int) string {
	filled := int(progress / 100 * float64(width))
	if filled > width {
		filled = width
	}
	empty := width - filled

	return strings.Repeat("█", filled) + strings.Repeat("░", empty)
}

func statusColor(status types.JobStatus) lipgloss.Color {
	switch status {
	case types.StatusCompleted:
		return lipgloss.Color("42")
	case types.StatusFailed:
		return lipgloss.Color("196")
	case types.StatusTranscoding, types.StatusDownloading, types.StatusUploading:
		return lipgloss.Color("226")
	case types.StatusPaused:
		return lipgloss.Color("208")
	case types.StatusCanceled:
		return lipgloss.Color("240")
	default:
		return lipgloss.Color("240")
	}
}

// addLog adds a log entry with timestamp and level
func (m *Model) addLog(level, message string) {
	timestamp := time.Now().Format("15:04:05")
	logEntry := fmt.Sprintf("[%s] %s: %s", timestamp, level, message)

	m.logs = append(m.logs, logEntry)

	// Keep only the last maxLogs entries
	if len(m.logs) > m.maxLogs {
		m.logs = m.logs[len(m.logs)-m.maxLogs:]
	}
}

// tailScannerLog reads new lines from the scanner log file and adds them to logs
func (m *Model) tailScannerLog() {
	// Check if log file exists
	fileInfo, err := os.Stat(m.scannerLogPath)
	if err != nil {
		return // File doesn't exist yet
	}

	// Check if file has shrunk (rotated or truncated)
	if fileInfo.Size() < m.scannerLogPos {
		m.scannerLogPos = 0
	}

	// Open file
	file, err := os.Open(m.scannerLogPath)
	if err != nil {
		return
	}
	defer file.Close()

	// Seek to last position
	_, err = file.Seek(m.scannerLogPos, 0)
	if err != nil {
		return
	}

	// Read new content
	content := make([]byte, fileInfo.Size()-m.scannerLogPos)
	n, err := file.Read(content)
	if err != nil && n == 0 {
		return
	}

	// Update position
	m.scannerLogPos += int64(n)

	// Split into lines and add to logs
	lines := strings.Split(string(content[:n]), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			// Add line directly (it already has timestamp and level from scanner)
			m.logs = append(m.logs, line)
		}
	}

	// Keep only the last maxLogs entries
	if len(m.logs) > m.maxLogs {
		m.logs = m.logs[len(m.logs)-m.maxLogs:]
	}
}
