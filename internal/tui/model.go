package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
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
	ViewJobs
	ViewSettings
	ViewScanner
	ViewLogs
)

// ProgressHistory tracks recent progress updates for bandwidth calculation
type ProgressHistory struct {
	Updates []types.ProgressUpdate // Circular buffer of recent updates
	MaxSize int                    // Maximum number of updates to keep
}

// Add adds a new progress update to the history
func (ph *ProgressHistory) Add(update types.ProgressUpdate) {
	if ph.Updates == nil {
		ph.MaxSize = 120 // Keep 120 updates (approximately 60 seconds of history for very stable bandwidth)
		ph.Updates = make([]types.ProgressUpdate, 0, ph.MaxSize)
	}

	// Add new update
	ph.Updates = append(ph.Updates, update)

	// Trim old updates if we exceed max size
	if len(ph.Updates) > ph.MaxSize {
		ph.Updates = ph.Updates[1:]
	}
}

// Latest returns the most recent progress update
func (ph *ProgressHistory) Latest() *types.ProgressUpdate {
	if len(ph.Updates) == 0 {
		return nil
	}
	return &ph.Updates[len(ph.Updates)-1]
}

// CalculateBandwidth calculates bandwidth in bytes/second using a time window
func (ph *ProgressHistory) CalculateBandwidth() float64 {
	if len(ph.Updates) < 2 {
		return 0
	}

	newest := ph.Updates[len(ph.Updates)-1]
	oldest := ph.Updates[0]

	// Only calculate bandwidth for download/upload stages with byte counts
	if newest.TotalBytes == 0 {
		return 0
	}

	bytesDelta := newest.BytesTransferred - oldest.BytesTransferred
	timeDelta := newest.Timestamp.Sub(oldest.Timestamp).Seconds()

	if timeDelta <= 0 || bytesDelta <= 0 {
		return 0
	}

	return float64(bytesDelta) / timeDelta
}

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
	progressData map[int64]*ProgressHistory // jobID -> progress history with bandwidth

	// Scanner state
	scanning     bool
	scanProgress scanner.ScanProgress

	// UI State
	selectedJob    int
	selectedSetting int
	statusMsg      string
	errorMsg       string
	jobsPanel      int  // 0=active jobs, 1=queued jobs
	activeJobsScrollOffset  int
	queuedJobsScrollOffset  int

	// Keyboard hints
	showHelp       bool
	configModified bool

	// Settings editing
	isEditingSettings   bool
	settingsInputs      []textinput.Model
	validationError     string
	configPath          string // Path to save config
	showPresetDropdown  bool   // Show preset dropdown menu
	presetDropdownIndex int    // Currently highlighted preset in dropdown

	// Job actions
	showJobActionDropdown  bool // Show job action dropdown menu
	jobActionDropdownIndex int  // Currently highlighted action in dropdown

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
		progressData:   make(map[int64]*ProgressHistory),
		configPath:     os.ExpandEnv("$HOME/transcoder/config.yaml"),
	}
	m.initSettingsInputs()
	m.addLog("INFO", "Transcoder TUI started")
	return m
}

// initSettingsInputs initializes the textinput fields for settings
func (m *Model) initSettingsInputs() {
	inputs := make([]textinput.Model, 9)

	// 0: Host
	inputs[0] = textinput.New()
	inputs[0].Placeholder = "Remote host"
	inputs[0].SetValue(m.cfg.Remote.Host)
	inputs[0].CharLimit = 100

	// 1: User
	inputs[1] = textinput.New()
	inputs[1].Placeholder = "SSH user"
	inputs[1].SetValue(m.cfg.Remote.User)
	inputs[1].CharLimit = 50

	// 2: Port
	inputs[2] = textinput.New()
	inputs[2].Placeholder = "SSH port"
	inputs[2].SetValue(fmt.Sprintf("%d", m.cfg.Remote.Port))
	inputs[2].CharLimit = 5

	// 3: SSH Key
	inputs[3] = textinput.New()
	inputs[3].Placeholder = "SSH key path"
	inputs[3].SetValue(m.cfg.Remote.SSHKey)
	inputs[3].CharLimit = 200

	// 4: Quality
	inputs[4] = textinput.New()
	inputs[4].Placeholder = "Quality (0-100)"
	inputs[4].SetValue(fmt.Sprintf("%d", m.cfg.Encoder.Quality))
	inputs[4].CharLimit = 3

	// 5: Preset
	inputs[5] = textinput.New()
	inputs[5].Placeholder = "Encoder preset"
	inputs[5].SetValue(m.cfg.Encoder.Preset)
	inputs[5].CharLimit = 20

	// 6: Max Workers
	inputs[6] = textinput.New()
	inputs[6].Placeholder = "Max workers"
	inputs[6].SetValue(fmt.Sprintf("%d", m.cfg.Workers.MaxWorkers))
	inputs[6].CharLimit = 3

	// 7: Work Directory
	inputs[7] = textinput.New()
	inputs[7].Placeholder = "Work directory"
	inputs[7].SetValue(m.cfg.Workers.WorkDir)
	inputs[7].CharLimit = 200

	// 8: Database Path
	inputs[8] = textinput.New()
	inputs[8].Placeholder = "Database path"
	inputs[8].SetValue(m.cfg.Database.Path)
	inputs[8].CharLimit = 200

	m.settingsInputs = inputs
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

	case tea.MouseMsg:
		return m.handleMouseClick(msg)

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
		update := types.ProgressUpdate(msg)
		if update.JobID > 0 {
			// Get or create progress history for this job
			if m.progressData[update.JobID] == nil {
				m.progressData[update.JobID] = &ProgressHistory{}
			}
			// Add update to history
			m.progressData[update.JobID].Add(update)
		}
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
	case ViewJobs:
		content = m.renderJobs()
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

	renderedContent := contentStyle.Render(content)

	// Overlay job action dropdown if visible
	if m.viewMode == ViewJobs && m.showJobActionDropdown {
		renderedContent = m.overlayJobActionDropdown(renderedContent)
	}

	return lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		renderedContent,
		footer,
	)
}

// overlayJobActionDropdown overlays the action dropdown on top of the content
func (m Model) overlayJobActionDropdown(content string) string {
	// Get current job
	var jobs []*types.TranscodeJob
	var scrollOffset int

	if m.jobsPanel == 0 {
		jobs = m.activeJobs
		scrollOffset = m.activeJobsScrollOffset
	} else {
		jobs = m.queuedJobs
		scrollOffset = m.queuedJobsScrollOffset
	}

	if m.selectedJob >= len(jobs) {
		return content
	}

	job := jobs[m.selectedJob]

	// Calculate Y position of selected job
	// header(0) + status(1) + empty(2) + box_border(3) + panel_tabs(4) + empty(5) + total_line(6) + empty(7) = jobs_start(8)
	baseY := 8

	// Calculate which visible job index this is (accounting for scroll)
	visibleJobIndex := m.selectedJob - scrollOffset
	if visibleJobIndex < 0 {
		return content // Job not visible, don't show dropdown
	}

	// Each job takes 4 lines
	jobY := baseY + (visibleJobIndex * 4)

	// Position dropdown below the selected job (after the 4 lines)
	dropdownY := jobY + 4

	// X position: indent from left edge
	dropdownX := 6

	// Render the dropdown
	dropdown := m.renderJobActionsDropdown(job)

	// Split content into lines
	contentLines := strings.Split(content, "\n")

	// Insert dropdown lines at the appropriate position
	dropdownLines := strings.Split(dropdown, "\n")

	// Create new content with dropdown overlaid
	var result strings.Builder
	for i, line := range contentLines {
		if i >= dropdownY && i < dropdownY+len(dropdownLines) {
			// Overlay this line with dropdown
			dropdownLine := dropdownLines[i-dropdownY]

			// Build the overlaid line: original line with dropdown on top
			var overlaidLine string
			if len(line) < dropdownX {
				// Pad line to reach dropdownX
				overlaidLine = line + strings.Repeat(" ", dropdownX-len(line)) + dropdownLine
			} else {
				// Take left part, add dropdown, keep any remainder
				leftPart := line[:dropdownX]
				endX := dropdownX + len(dropdownLine)
				if endX < len(line) {
					overlaidLine = leftPart + dropdownLine + line[endX:]
				} else {
					overlaidLine = leftPart + dropdownLine
				}
			}
			result.WriteString(overlaidLine)
		} else {
			result.WriteString(line)
		}
		if i < len(contentLines)-1 {
			result.WriteString("\n")
		}
	}

	return result.String()
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
		m.viewMode = ViewJobs
		m.refreshData()
		return m, nil

	case "3":
		m.viewMode = ViewSettings
		return m, nil

	case "4":
		m.viewMode = ViewScanner
		return m, nil

	case "5":
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
	case ViewJobs:
		return m.handleJobListKeys(msg)
	case ViewSettings:
		return m.handleSettingsKeys(msg)
	case ViewLogs:
		return m.handleLogsKeys(msg)
	}

	return m, nil
}

// handleMouseClick processes mouse clicks and wheel events
func (m Model) handleMouseClick(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	// Handle mouse wheel scrolling
	if msg.Type == tea.MouseWheelUp {
		switch m.viewMode {
		case ViewLogs:
			if m.logScrollOffset > 0 {
				m.logScrollOffset -= 3 // Scroll up 3 lines
				if m.logScrollOffset < 0 {
					m.logScrollOffset = 0
				}
			}
			return m, nil
		case ViewJobs:
			// Scroll job list up
			if m.jobsPanel == 0 {
				if m.activeJobsScrollOffset > 0 {
					m.activeJobsScrollOffset--
				}
			} else {
				if m.queuedJobsScrollOffset > 0 {
					m.queuedJobsScrollOffset--
				}
			}
			return m, nil
		}
		return m, nil
	}

	if msg.Type == tea.MouseWheelDown {
		switch m.viewMode {
		case ViewLogs:
			availableHeight := m.height - 7
			if availableHeight < 1 {
				availableHeight = 10
			}
			maxScrollOffset := len(m.logs) - availableHeight
			if maxScrollOffset < 0 {
				maxScrollOffset = 0
			}
			if m.logScrollOffset < maxScrollOffset {
				m.logScrollOffset += 3 // Scroll down 3 lines
				if m.logScrollOffset > maxScrollOffset {
					m.logScrollOffset = maxScrollOffset
				}
			}
			return m, nil
		case ViewJobs:
			// Scroll job list down
			visibleHeight := m.calculateVisibleJobsHeight()
			if m.jobsPanel == 0 {
				maxScroll := len(m.activeJobs) - visibleHeight
				if maxScroll < 0 {
					maxScroll = 0
				}
				if m.activeJobsScrollOffset < maxScroll {
					m.activeJobsScrollOffset++
				}
			} else {
				maxScroll := len(m.queuedJobs) - visibleHeight
				if maxScroll < 0 {
					maxScroll = 0
				}
				if m.queuedJobsScrollOffset < maxScroll {
					m.queuedJobsScrollOffset++
				}
			}
			return m, nil
		}
		return m, nil
	}

	// Only handle left clicks below
	if msg.Type != tea.MouseLeft {
		return m, nil
	}

	// Calculate tab positions in the header
	// Header format: "📹 Video Transcoder TUI" + spacing + "[1] Dashboard  [2] Jobs  [3] Settings  [4] Scanner  [5] Logs"

	tabs := []string{
		"[1] Dashboard",
		"[2] Jobs",
		"[3] Settings",
		"[4] Scanner",
		"[5] Logs",
	}

	// Calculate where tabs start on the screen
	// Tabs are right-aligned
	tabsWidth := 0
	for i, tab := range tabs {
		tabsWidth += len(tab)
		if i < len(tabs)-1 {
			tabsWidth += 2 // spacing between tabs
		}
	}

	tabsStartX := m.width - tabsWidth

	// Check if click was in header row (y == 0)
	if msg.Y == 0 && msg.X >= tabsStartX {
		// Calculate which tab was clicked
		currentX := tabsStartX
		for i, tab := range tabs {
			tabEnd := currentX + len(tab)
			if msg.X >= currentX && msg.X < tabEnd {
				// Tab clicked!
				m.viewMode = ViewMode(i)
				m.refreshData()
				return m, nil
			}
			currentX = tabEnd + 2 // Move to next tab (including spacing)
		}
	}

	// Handle clicks on Jobs view panel switcher
	// Panel switcher is at row 3 (header=0, status=1, empty=2, box_border=3, panel_tabs=4)
	if m.viewMode == ViewJobs && msg.Y == 4 {
		// Panel tabs: " 🎬 Active Jobs " + "  " + " 📋 Queued Jobs "
		// Starting X is at border (around x=3 due to box padding)
		activeTabText := " 🎬 Active Jobs "
		queuedTabText := " 📋 Queued Jobs "

		// Account for box border and padding (typically starts at x ~= 3)
		panelTabsStartX := 3

		activeTabEnd := panelTabsStartX + len(activeTabText)
		queuedTabStart := activeTabEnd + 2 // 2 spaces between tabs
		queuedTabEnd := queuedTabStart + len(queuedTabText)

		if msg.X >= panelTabsStartX && msg.X < activeTabEnd {
			// Active Jobs tab clicked
			m.jobsPanel = 0
			m.selectedJob = 0
			m.activeJobsScrollOffset = 0
			return m, nil
		} else if msg.X >= queuedTabStart && msg.X < queuedTabEnd {
			// Queued Jobs tab clicked
			m.jobsPanel = 1
			m.selectedJob = 0
			m.queuedJobsScrollOffset = 0
			return m, nil
		}
	}

	// Handle clicks on individual jobs in Jobs view
	if m.viewMode == ViewJobs && msg.Y >= 8 {
		// Calculate which job was clicked based on dynamic layout
		jobIndex := m.calculateJobIndexFromClick(msg.Y)
		if jobIndex >= 0 {
			m.selectedJob = jobIndex
			return m, nil
		}
	}

	// Handle clicks on Settings fields
	// Settings layout: header(0), status(1), empty(2), box_border(3), title(4), empty(5)
	// Remote Config header(6), Host(7), User(8), Port(9), SSH Key(10)
	// Encoder header(11), Codec(12), Quality(13), Preset(14)
	// Worker header(15), Max Workers(16), Work Dir(17)
	// Database header(18), DB Path(19)
	if m.viewMode == ViewSettings && msg.Y >= 7 {
		// Map Y positions to setting indices
		// Remote: Host=7, User=8, Port=9, SSH Key=10
		// Encoder: Quality=13, Preset=14
		// Worker: Max Workers=16, Work Dir=17
		// Database: DB Path=19

		settingMapping := map[int]int{
			7: 0,  // Host
			8: 1,  // User
			9: 2,  // Port
			10: 3, // SSH Key
			13: 4, // Quality
			14: 5, // Preset
			16: 6, // Max Workers
			17: 7, // Work Dir
			19: 8, // Database Path
		}

		if settingIndex, ok := settingMapping[msg.Y]; ok {
			m.selectedSetting = settingIndex
			return m, nil
		}
	}

	return m, nil
}

// calculateJobIndexFromClick calculates which job was clicked based on Y position
func (m Model) calculateJobIndexFromClick(clickY int) int {
	// Get current job list based on panel
	var jobs []*types.TranscodeJob
	var scrollOffset int

	if m.jobsPanel == 0 {
		jobs = m.activeJobs
		scrollOffset = m.activeJobsScrollOffset
	} else {
		jobs = m.queuedJobs
		scrollOffset = m.queuedJobsScrollOffset
	}

	visibleHeight := m.calculateVisibleJobsHeight()

	// Calculate starting Y position
	// header(0) + status(1) + empty(2) + box_border(3) + panel_tabs(4) + empty(5) + total_line(6) + empty(7) = jobs_start(8)
	currentY := 8

	// Calculate visible window
	startIdx := scrollOffset
	endIdx := startIdx + visibleHeight
	if endIdx > len(jobs) {
		endIdx = len(jobs)
	}

	// Iterate through visible jobs and track Y positions
	for i := startIdx; i < endIdx; i++ {
		// Each job has:
		// - Job info line (1 line)
		// - Status line (1 line)
		// - Progress line (1 line)
		// - Spacing (1 line)
		jobStartY := currentY
		jobEndY := currentY + 4

		// Check if click falls within this job's range
		// Note: Dropdown floats and doesn't affect click detection
		if clickY >= jobStartY && clickY < jobEndY {
			return i
		}

		currentY = jobEndY
	}

	return -1 // No job clicked
}

// handleJobListKeys handles keys in job list views
func (m Model) handleJobListKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Handle job action dropdown navigation
	if m.showJobActionDropdown {
		return m.handleJobActionDropdown(msg)
	}

	// Get current job list based on panel
	var jobs []*types.TranscodeJob
	var scrollOffset *int

	// In Jobs view, switch based on panel
	if m.jobsPanel == 0 {
		jobs = m.activeJobs
		scrollOffset = &m.activeJobsScrollOffset
	} else {
		jobs = m.queuedJobs
		scrollOffset = &m.queuedJobsScrollOffset
	}

	switch msg.String() {
	case "tab":
		// Switch panels in Jobs view only
		if m.viewMode == ViewJobs {
			m.jobsPanel = (m.jobsPanel + 1) % 2
			m.selectedJob = 0  // Reset selection when switching panels
		}

	case "up", "k":
		if m.selectedJob > 0 {
			m.selectedJob--
			// Adjust scroll offset if needed
			if scrollOffset != nil && m.selectedJob < *scrollOffset {
				*scrollOffset = m.selectedJob
			}
		}

	case "down", "j":
		if m.selectedJob < len(jobs)-1 {
			m.selectedJob++
			// Adjust scroll offset if needed (calculate visible height)
			if scrollOffset != nil {
				visibleHeight := m.calculateVisibleJobsHeight()
				if m.selectedJob >= *scrollOffset+visibleHeight {
					*scrollOffset = m.selectedJob - visibleHeight + 1
				}
			}
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
		// Show action dropdown for selected job
		if m.selectedJob < len(jobs) {
			m.showJobActionDropdown = true
			m.jobActionDropdownIndex = 0
			return m, nil
		}

	case "a":
		// Add/Queue jobs for transcoding (only in Jobs view)
		if m.viewMode == ViewJobs {
			count, err := m.db.QueueJobsForTranscoding(100)
			if err != nil {
				m.errorMsg = fmt.Sprintf("Failed to queue jobs: %v", err)
			} else {
				m.statusMsg = fmt.Sprintf("Queued %d jobs for transcoding", count)
				m.addLog("INFO", fmt.Sprintf("Queued %d jobs", count))
				m.refreshData()
			}
		}

	case "d":
		// Delete selected job (only in Jobs view, terminal states only)
		if m.viewMode == ViewJobs && m.selectedJob < len(jobs) {
			job := jobs[m.selectedJob]
			if job.Status == types.StatusQueued ||
				job.Status == types.StatusFailed ||
				job.Status == types.StatusCompleted ||
				job.Status == types.StatusCanceled {
				if err := m.db.DeleteJob(job.ID); err != nil {
					m.errorMsg = fmt.Sprintf("Failed to delete job: %v", err)
				} else {
					m.statusMsg = fmt.Sprintf("Deleted job #%d", job.ID)
					m.addLog("INFO", fmt.Sprintf("Deleted job #%d (%s)", job.ID, job.FileName))
					m.refreshData()
				}
			} else {
				m.errorMsg = "Cannot delete job in progress (cancel it first)"
			}
		}

	case "K":
		// Kill/Force-cancel selected job (only in Jobs view)
		if m.viewMode == ViewJobs && m.selectedJob < len(jobs) {
			job := jobs[m.selectedJob]
			if job.Status != types.StatusCompleted &&
				job.Status != types.StatusFailed &&
				job.Status != types.StatusCanceled {
				if err := m.db.KillJob(job.ID); err != nil {
					m.errorMsg = fmt.Sprintf("Failed to kill job: %v", err)
				} else {
					m.statusMsg = fmt.Sprintf("Killed job #%d", job.ID)
					m.addLog("WARN", fmt.Sprintf("Killed job #%d (%s)", job.ID, job.FileName))
					m.workerPool.CancelJob(job.ID)
					m.refreshData()
				}
			} else {
				m.errorMsg = "Job already terminated"
			}
		}
	}

	return m, nil
}

// handleJobActionDropdown handles keys when the job action dropdown is shown
func (m Model) handleJobActionDropdown(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Get current job
	var jobs []*types.TranscodeJob
	if m.jobsPanel == 0 {
		jobs = m.activeJobs
	} else {
		jobs = m.queuedJobs
	}

	if m.selectedJob >= len(jobs) {
		m.showJobActionDropdown = false
		return m, nil
	}

	job := jobs[m.selectedJob]
	actions := m.getJobActions(job)

	switch msg.String() {
	case "up", "k":
		if m.jobActionDropdownIndex > 0 {
			m.jobActionDropdownIndex--
		}
		return m, nil
	case "down", "j":
		if m.jobActionDropdownIndex < len(actions)-1 {
			m.jobActionDropdownIndex++
		}
		return m, nil
	case "enter":
		// Execute selected action
		if m.jobActionDropdownIndex < len(actions) {
			m.executeJobAction(job, actions[m.jobActionDropdownIndex].action)
		}
		m.showJobActionDropdown = false
		return m, nil
	case "esc":
		// Cancel dropdown
		m.showJobActionDropdown = false
		return m, nil
	}
	return m, nil
}

// JobAction represents an available action for a job
type JobAction struct {
	action      string
	label       string
	description string
}

// getJobActions returns available actions for a job based on its status
func (m Model) getJobActions(job *types.TranscodeJob) []JobAction {
	var actions []JobAction

	switch job.Status {
	case types.StatusQueued:
		actions = append(actions, JobAction{
			action:      "delete",
			label:       "Delete",
			description: "Remove from queue",
		})
		actions = append(actions, JobAction{
			action:      "priority",
			label:       "Adjust Priority",
			description: "Change queue priority",
		})

	case types.StatusDownloading, types.StatusTranscoding, types.StatusUploading:
		actions = append(actions, JobAction{
			action:      "pause",
			label:       "Pause",
			description: "Pause current processing",
		})
		actions = append(actions, JobAction{
			action:      "cancel",
			label:       "Cancel",
			description: "Cancel and requeue",
		})
		actions = append(actions, JobAction{
			action:      "kill",
			label:       "Kill (Force)",
			description: "Force terminate immediately",
		})

	case types.StatusPaused:
		actions = append(actions, JobAction{
			action:      "resume",
			label:       "Resume",
			description: "Continue processing",
		})
		actions = append(actions, JobAction{
			action:      "cancel",
			label:       "Cancel",
			description: "Cancel and requeue",
		})
		actions = append(actions, JobAction{
			action:      "kill",
			label:       "Kill (Force)",
			description: "Force terminate immediately",
		})

	case types.StatusCompleted, types.StatusFailed, types.StatusCanceled:
		actions = append(actions, JobAction{
			action:      "delete",
			label:       "Delete",
			description: "Remove from list",
		})
		if job.Status == types.StatusFailed {
			actions = append(actions, JobAction{
				action:      "retry",
				label:       "Retry",
				description: "Requeue for processing",
			})
		}
	}

	return actions
}

// executeJobAction executes the selected action on a job
func (m *Model) executeJobAction(job *types.TranscodeJob, action string) {
	switch action {
	case "pause":
		m.workerPool.PauseJob(job.ID)
		m.statusMsg = fmt.Sprintf("Pausing job #%d", job.ID)

	case "resume":
		m.db.ResumeJob(job.ID)
		m.statusMsg = fmt.Sprintf("Resuming job #%d", job.ID)

	case "cancel":
		m.workerPool.CancelJob(job.ID)
		m.statusMsg = fmt.Sprintf("Canceling job #%d", job.ID)

	case "kill":
		if err := m.db.KillJob(job.ID); err != nil {
			m.errorMsg = fmt.Sprintf("Failed to kill job: %v", err)
		} else {
			m.statusMsg = fmt.Sprintf("Killed job #%d", job.ID)
			m.addLog("INFO", fmt.Sprintf("Force-killed job #%d (%s)", job.ID, job.FileName))
			m.refreshData()
		}

	case "delete":
		if err := m.db.DeleteJob(job.ID); err != nil {
			m.errorMsg = fmt.Sprintf("Failed to delete job: %v", err)
		} else {
			m.statusMsg = fmt.Sprintf("Deleted job #%d", job.ID)
			m.addLog("INFO", fmt.Sprintf("Deleted job #%d (%s)", job.ID, job.FileName))
			m.refreshData()
		}

	case "retry":
		if err := m.db.UpdateJobStatus(job.ID, types.StatusQueued, "", 0); err != nil {
			m.errorMsg = fmt.Sprintf("Failed to requeue job: %v", err)
		} else {
			m.statusMsg = fmt.Sprintf("Requeued job #%d", job.ID)
			m.addLog("INFO", fmt.Sprintf("Requeued job #%d for retry", job.ID))
			m.refreshData()
		}

	case "priority":
		// TODO: Implement priority adjustment UI
		m.statusMsg = "Priority adjustment not yet implemented"
	}
}

// applySettingValue validates and applies the textinput value to the config
func (m *Model) applySettingValue(index int) error {
	value := m.settingsInputs[index].Value()

	switch index {
	case 0: // Host
		if value == "" {
			return fmt.Errorf("host cannot be empty")
		}
		m.cfg.Remote.Host = value
		m.configModified = true

	case 1: // User
		if value == "" {
			return fmt.Errorf("user cannot be empty")
		}
		m.cfg.Remote.User = value
		m.configModified = true

	case 2: // Port
		var port int
		if _, err := fmt.Sscanf(value, "%d", &port); err != nil {
			return fmt.Errorf("port must be a number")
		}
		if port < 1 || port > 65535 {
			return fmt.Errorf("port must be between 1 and 65535")
		}
		m.cfg.Remote.Port = port
		m.configModified = true

	case 3: // SSH Key
		if value == "" {
			return fmt.Errorf("SSH key path cannot be empty")
		}
		m.cfg.Remote.SSHKey = value
		m.configModified = true

	case 4: // Quality
		var quality int
		if _, err := fmt.Sscanf(value, "%d", &quality); err != nil {
			return fmt.Errorf("quality must be a number")
		}
		if quality < 0 || quality > 100 {
			return fmt.Errorf("quality must be between 0 and 100")
		}
		m.cfg.Encoder.Quality = quality
		m.configModified = true

	case 5: // Preset
		validPresets := []string{"ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow"}
		valid := false
		for _, p := range validPresets {
			if value == p {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("preset must be one of: ultrafast, superfast, veryfast, faster, fast, medium, slow, slower, veryslow")
		}
		m.cfg.Encoder.Preset = value
		m.configModified = true

	case 6: // Max Workers
		var workers int
		if _, err := fmt.Sscanf(value, "%d", &workers); err != nil {
			return fmt.Errorf("max workers must be a number")
		}
		if workers < 0 {
			return fmt.Errorf("max workers cannot be negative")
		}
		// Scale the worker pool
		m.workerPool.ScaleWorkers(workers)
		m.configModified = true

	case 7: // Work Directory
		if value == "" {
			return fmt.Errorf("work directory cannot be empty")
		}
		m.cfg.Workers.WorkDir = value
		m.configModified = true

	case 8: // Database Path
		if value == "" {
			return fmt.Errorf("database path cannot be empty")
		}
		m.cfg.Database.Path = value
		m.configModified = true
	}

	return nil
}

// revertSettingValue reverts the textinput to the current config value
func (m *Model) revertSettingValue(index int) {
	switch index {
	case 0:
		m.settingsInputs[index].SetValue(m.cfg.Remote.Host)
	case 1:
		m.settingsInputs[index].SetValue(m.cfg.Remote.User)
	case 2:
		m.settingsInputs[index].SetValue(fmt.Sprintf("%d", m.cfg.Remote.Port))
	case 3:
		m.settingsInputs[index].SetValue(m.cfg.Remote.SSHKey)
	case 4:
		m.settingsInputs[index].SetValue(fmt.Sprintf("%d", m.cfg.Encoder.Quality))
	case 5:
		m.settingsInputs[index].SetValue(m.cfg.Encoder.Preset)
	case 6:
		m.settingsInputs[index].SetValue(fmt.Sprintf("%d", m.cfg.Workers.MaxWorkers))
	case 7:
		m.settingsInputs[index].SetValue(m.cfg.Workers.WorkDir)
	case 8:
		m.settingsInputs[index].SetValue(m.cfg.Database.Path)
	}
}

// handleSettingsKeys handles keys in settings view
func (m Model) handleSettingsKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Handle preset dropdown navigation
	if m.showPresetDropdown {
		presets := []string{"ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow"}

		switch msg.String() {
		case "up", "k":
			if m.presetDropdownIndex > 0 {
				m.presetDropdownIndex--
			}
			return m, nil
		case "down", "j":
			if m.presetDropdownIndex < len(presets)-1 {
				m.presetDropdownIndex++
			}
			return m, nil
		case "enter":
			// Select the highlighted preset
			m.cfg.Encoder.Preset = presets[m.presetDropdownIndex]
			m.settingsInputs[5].SetValue(presets[m.presetDropdownIndex])
			m.showPresetDropdown = false
			m.isEditingSettings = false
			m.configModified = true
			m.statusMsg = fmt.Sprintf("Preset set to: %s", presets[m.presetDropdownIndex])
			return m, nil
		case "esc":
			// Cancel dropdown
			m.showPresetDropdown = false
			m.isEditingSettings = false
			return m, nil
		}
		return m, nil
	}

	// If we're editing a field, handle it specially
	if m.isEditingSettings {
		switch msg.String() {
		case "enter":
			// Save the current value
			if err := m.applySettingValue(m.selectedSetting); err != nil {
				m.validationError = err.Error()
				return m, nil
			}
			m.validationError = ""
			m.isEditingSettings = false
			m.settingsInputs[m.selectedSetting].Blur()
			m.statusMsg = "Setting updated"
			return m, nil

		case "esc":
			// Cancel editing and revert to original value
			m.isEditingSettings = false
			m.settingsInputs[m.selectedSetting].Blur()
			m.validationError = ""
			// Revert to config value
			m.revertSettingValue(m.selectedSetting)
			return m, nil

		default:
			// Pass key to textinput
			var cmd tea.Cmd
			m.settingsInputs[m.selectedSetting], cmd = m.settingsInputs[m.selectedSetting].Update(msg)
			return m, cmd
		}
	}

	// Not editing - handle navigation and other keys
	switch msg.String() {
	case "up", "k":
		if m.selectedSetting > 0 {
			m.selectedSetting--
		}

	case "down", "j":
		if m.selectedSetting < 8 { // 9 editable settings (0-8)
			m.selectedSetting++
		}

	case "enter":
		// Start editing the selected field
		if m.selectedSetting == 5 {
			// Preset field - show dropdown instead
			m.isEditingSettings = true
			m.showPresetDropdown = true
			m.validationError = ""

			// Find current preset index
			presets := []string{"ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow"}
			currentPreset := m.cfg.Encoder.Preset
			for i, p := range presets {
				if p == currentPreset {
					m.presetDropdownIndex = i
					break
				}
			}
			return m, nil
		} else {
			// Regular textinput editing
			m.isEditingSettings = true
			m.settingsInputs[m.selectedSetting].Focus()
			m.validationError = ""
			return m, textinput.Blink
		}

	case "ctrl+s":
		// Save config to file
		if err := m.cfg.Save(m.configPath); err != nil {
			m.errorMsg = fmt.Sprintf("Failed to save config: %v", err)
		} else {
			m.statusMsg = "Configuration saved to file!"
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

	// Get recent completed/failed jobs for history view
	m.recentJobs, err = m.db.GetCompletedJobs(100)
	if err != nil {
		m.errorMsg = fmt.Sprintf("Failed to get completed jobs: %v", err)
	}

	m.lastUpdate = time.Now()
}

// calculateVisibleJobsHeight calculates how many job items can fit in the Jobs view
func (m Model) calculateVisibleJobsHeight() int {
	// Approximate calculation:
	// Screen height - header (3 lines) - footer (3 lines) - panel title (3 lines) - help text (2 lines) - margins (4 lines)
	// Each job takes 4 lines (job line + status line + progress line + spacing)
	availableHeight := m.height - 15
	if availableHeight < 4 {
		availableHeight = 4
	}
	return availableHeight / 4 // Each job takes 4 lines with enhanced selection
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
			Border(lipgloss.DoubleBorder()).
			BorderForeground(lipgloss.Color("62")).
			Padding(1, 2).
			MarginBottom(2).
			MarginRight(2)

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

func formatProgress(progress float64, stage types.ProcessingStage) string {
	bar := coloredProgressBar(progress, 20, stage)
	return fmt.Sprintf("[%s] %.1f%%", bar, progress)
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func progressBar(progress float64, width int) string {
	filled := int(progress / 100 * float64(width))
	if filled > width {
		filled = width
	}
	empty := width - filled

	return strings.Repeat("█", filled) + strings.Repeat("░", empty)
}

// coloredProgressBar creates a colored progress bar based on the stage
func coloredProgressBar(progress float64, width int, stage types.ProcessingStage) string {
	filled := int(progress / 100 * float64(width))
	if filled > width {
		filled = width
	}
	empty := width - filled

	// Create colored filled portion
	color := stageColor(stage)
	filledStr := lipgloss.NewStyle().Foreground(color).Render(strings.Repeat("█", filled))
	emptyStr := strings.Repeat("░", empty)

	return filledStr + emptyStr
}

func statusColor(status types.JobStatus) lipgloss.Color {
	switch status {
	case types.StatusCompleted:
		return lipgloss.Color("42") // Green
	case types.StatusFailed:
		return lipgloss.Color("196") // Red
	case types.StatusDownloading:
		return lipgloss.Color("33") // Blue
	case types.StatusTranscoding:
		return lipgloss.Color("51") // Cyan
	case types.StatusUploading:
		return lipgloss.Color("42") // Green
	case types.StatusPaused:
		return lipgloss.Color("208") // Orange
	case types.StatusCanceled:
		return lipgloss.Color("240") // Dark gray
	default:
		return lipgloss.Color("240") // Dark gray
	}
}

// stageColor returns color for a specific processing stage
func stageColor(stage types.ProcessingStage) lipgloss.Color {
	switch stage {
	case types.StageDownload:
		return lipgloss.Color("33") // Blue
	case types.StageTranscode:
		return lipgloss.Color("51") // Cyan
	case types.StageUpload:
		return lipgloss.Color("42") // Green
	case types.StageValidate:
		return lipgloss.Color("214") // Amber
	default:
		return lipgloss.Color("240") // Dark gray
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
