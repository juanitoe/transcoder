package scanner

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"transcoder/internal/checksum"
	"transcoder/internal/config"
	"transcoder/internal/database"
	"transcoder/internal/types"
)

// Scanner handles remote media library scanning
type Scanner struct {
	cfg          *config.Config
	db           *database.DB
	sshClient    *ssh.Client  // Main SSH client
	sftpClient   *sftp.Client // Main SFTP client for directory operations
	sshPool      *sshPool     // Pool of SSH clients for parallel FFprobe
	progress     ScanProgress
	progressMu   sync.Mutex // Protects progress updates from concurrent workers
	logFile      *os.File
	logWriter    *bufio.Writer      // Buffered writer for efficient logging
	checksumAlgo checksum.Algorithm // Detected remote checksum algorithm
}

// sshPool manages a pool of SSH connections for parallel operations
type sshPool struct {
	clients   []*ssh.Client
	available chan *ssh.Client
	cfg       *config.Config
}

// newSSHPool creates a new SSH connection pool
func newSSHPool(cfg *config.Config, size int) (*sshPool, error) {
	pool := &sshPool{
		clients:   make([]*ssh.Client, 0, size),
		available: make(chan *ssh.Client, size),
		cfg:       cfg,
	}

	// Create pool of SSH clients
	for i := 0; i < size; i++ {
		client, err := createSSHClient(cfg)
		if err != nil {
			// Clean up any clients we created
			pool.Close()
			return nil, fmt.Errorf("failed to create SSH client %d: %w", i, err)
		}
		pool.clients = append(pool.clients, client)
		pool.available <- client
	}

	return pool, nil
}

// get retrieves an available SSH client from the pool
func (p *sshPool) get() *ssh.Client {
	return <-p.available
}

// put returns an SSH client to the pool
func (p *sshPool) put(client *ssh.Client) {
	p.available <- client
}

// Close closes all SSH clients in the pool
func (p *sshPool) Close() {
	close(p.available)
	for _, client := range p.clients {
		if client != nil {
			_ = client.Close()
		}
	}
}

// workItem represents a file that needs metadata extraction
type workItem struct {
	path         string
	size         int64
	existingFile *types.MediaFile // nil for new files
}

// workResult represents the result of processing a work item
type workResult struct {
	path       string
	metadata   *types.MediaFile
	isNew      bool  // true if file is new, false if update
	existingID int64 // ID of existing file if update
	size       int64 // File size for progress tracking
	err        error
}

// New creates a new Scanner instance
func New(cfg *config.Config, db *database.DB) (*Scanner, error) {
	// Open log file
	logPath := os.ExpandEnv(cfg.Logging.ScannerLog)
	if strings.HasPrefix(logPath, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		logPath = filepath.Join(home, logPath[2:])
	}

	// Ensure log directory exists
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	// Open log file in append mode
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}

	return &Scanner{
		cfg:       cfg,
		db:        db,
		logFile:   logFile,
		logWriter: bufio.NewWriter(logFile),
	}, nil
}

// createSSHClient creates a single SSH client connection
func createSSHClient(cfg *config.Config) (*ssh.Client, error) {
	// Try to use SSH agent first (for passphrase-protected keys)
	var authMethods []ssh.AuthMethod

	// Try SSH agent
	if agentConn, err := net.Dial("unix", os.Getenv("SSH_AUTH_SOCK")); err == nil {
		authMethods = append(authMethods, ssh.PublicKeysCallback(agent.NewClient(agentConn).Signers))
	}

	// Try loading key file if agent failed or as fallback
	if len(authMethods) == 0 {
		keyPath := os.ExpandEnv(cfg.Remote.SSHKey)
		if strings.HasPrefix(keyPath, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("failed to get home directory: %w", err)
			}
			keyPath = filepath.Join(home, keyPath[2:])
		}

		// Read private key
		keyBytes, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read SSH key: %w", err)
		}

		// Parse private key (will fail if passphrase-protected)
		signer, err := ssh.ParsePrivateKey(keyBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse SSH key: %w (try using ssh-add to add your key to the agent)", err)
		}

		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}

	// Configure SSH client
	sshConfig := &ssh.ClientConfig{
		User:            cfg.Remote.User,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // TODO: Use known_hosts
		Timeout:         time.Duration(cfg.Remote.SSHTimeout) * time.Second,
	}

	// Connect to SSH server
	addr := fmt.Sprintf("%s:%d", cfg.Remote.Host, cfg.Remote.Port)
	sshClient, err := ssh.Dial("tcp", addr, sshConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to SSH server: %w", err)
	}

	// Set up keepalive if configured
	if cfg.Remote.SSHKeepalive > 0 {
		go func() {
			ticker := time.NewTicker(time.Duration(cfg.Remote.SSHKeepalive) * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				_, _, err := sshClient.SendRequest("keepalive@openssh.com", true, nil)
				if err != nil {
					return // Connection closed
				}
			}
		}()
	}

	return sshClient, nil
}

// Connect establishes connection to remote server (or validates local paths in local mode)
func (s *Scanner) Connect(ctx context.Context) error {
	// In local mode, just validate that paths exist
	if s.cfg.IsLocalMode() {
		s.logDebug("Operating in local mode - validating media paths")
		for _, path := range s.cfg.GetMediaPaths() {
			expandedPath := expandPath(path)
			if _, err := os.Stat(expandedPath); os.IsNotExist(err) {
				return fmt.Errorf("local media path does not exist: %s", expandedPath)
			}
			s.logDebug("Validated local path: %s", expandedPath)
		}
		s.checksumAlgo = checksum.AlgoXXH64 // Use xxhash for local files
		s.logDebug("Local mode initialized")
		return nil
	}

	// Remote mode: establish SSH/SFTP connection
	s.logDebug("Connecting to SSH server: %s@%s:%d", s.cfg.Remote.User, s.cfg.Remote.Host, s.cfg.Remote.Port)

	// Create main SSH client
	sshClient, err := createSSHClient(s.cfg)
	if err != nil {
		return err
	}
	s.sshClient = sshClient
	s.logDebug("SSH connection established")

	// Create SFTP client with optional buffer size configuration
	s.logDebug("Creating SFTP client")
	var sftpClient *sftp.Client
	if s.cfg.Remote.SFTPBufferSize > 0 && s.cfg.Remote.SFTPBufferSize != 32768 {
		// Use custom buffer size (unchecked for larger sizes)
		// MaxPacketUnchecked allows sizes > 32KB for trusted servers
		sftpClient, err = sftp.NewClient(sshClient,
			sftp.MaxPacketUnchecked(s.cfg.Remote.SFTPBufferSize))
		s.logDebug("SFTP client created with buffer size: %d bytes", s.cfg.Remote.SFTPBufferSize)
	} else {
		// Use default buffer size
		sftpClient, err = sftp.NewClient(sshClient)
		s.logDebug("SFTP client created with default buffer size")
	}
	if err != nil {
		_ = s.sshClient.Close()
		return fmt.Errorf("failed to create SFTP client: %w", err)
	}
	s.sftpClient = sftpClient
	s.logDebug("SFTP client created successfully")

	// Create SSH connection pool for parallel FFprobe operations
	// Use a configurable pool size per scanner to avoid overwhelming SSH server
	// when multiple workers are active (e.g., 16 workers × 16 connections = 256 connections!)
	poolSize := s.cfg.Remote.SSHPoolSize
	s.logDebug("Creating SSH connection pool with %d connections", poolSize)
	pool, err := newSSHPool(s.cfg, poolSize)
	if err != nil {
		_ = s.sftpClient.Close()
		_ = s.sshClient.Close()
		return fmt.Errorf("failed to create SSH connection pool: %w", err)
	}
	s.sshPool = pool
	s.logDebug("SSH connection pool created successfully")

	// Detect available checksum algorithm on remote
	s.checksumAlgo = s.detectRemoteChecksumAlgo()
	s.logDebug("Remote checksum algorithm: %s", s.checksumAlgo)

	return nil
}

// detectRemoteChecksumAlgo checks which checksum tool is available on remote
func (s *Scanner) detectRemoteChecksumAlgo() checksum.Algorithm {
	session, err := s.sshClient.NewSession()
	if err != nil {
		s.logDebug("Failed to create session for checksum detection: %v", err)
		return checksum.AlgoMD5 // Default fallback
	}
	defer func() { _ = session.Close() }()

	output, err := session.Output(checksum.DetectRemoteCommand())
	if err != nil {
		s.logDebug("Failed to detect checksum tool: %v", err)
		return checksum.AlgoMD5 // Default fallback
	}

	return checksum.ParseDetectionOutput(string(output))
}

// GetChecksumAlgo returns the detected remote checksum algorithm
func (s *Scanner) GetChecksumAlgo() checksum.Algorithm {
	return s.checksumAlgo
}

// Close closes the SSH/SFTP connection
func (s *Scanner) Close() error {
	if s.sshPool != nil {
		s.sshPool.Close()
	}
	if s.sftpClient != nil {
		_ = s.sftpClient.Close()
	}
	if s.sshClient != nil {
		_ = s.sshClient.Close()
	}
	if s.logWriter != nil {
		_ = s.logWriter.Flush() // Flush buffered logs before closing
	}
	if s.logFile != nil {
		_ = s.logFile.Close()
	}
	return nil
}

// ScanProgress represents scan progress information
type ScanProgress struct {
	CurrentPath  string
	FilesScanned int
	FilesAdded   int
	FilesUpdated int
	BytesScanned int64
	TotalFiles   int   // Total files discovered (for percentage calculation)
	TotalBytes   int64 // Total bytes discovered (for percentage calculation)
	ErrorCount   int
	LastError    error
	FilesPerSec  float64 // Scan rate in files/second
	BytesPerSec  float64 // Scan rate in bytes/second
}

// progressThrottler controls how often progress updates are sent
type progressThrottler struct {
	lastUpdate     time.Time
	lastFileCount  int
	updateInterval time.Duration // Minimum time between updates
	fileInterval   int           // Minimum files between updates
}

// ProgressCallback is called periodically during scanning
type ProgressCallback func(progress ScanProgress)

// newProgressThrottler creates a new throttler
func newProgressThrottler() *progressThrottler {
	return &progressThrottler{
		lastUpdate:     time.Now(),
		updateInterval: 2 * time.Second, // Update at most every 2 seconds
		fileInterval:   10,              // Or every 10 files
	}
}

// shouldUpdate returns true if we should send a progress update
func (pt *progressThrottler) shouldUpdate(filesScanned int, force bool) bool {
	if force {
		return true
	}

	now := time.Now()
	timeSinceUpdate := now.Sub(pt.lastUpdate)
	filesSinceUpdate := filesScanned - pt.lastFileCount

	// Update if enough time has passed OR enough files processed
	if timeSinceUpdate >= pt.updateInterval || filesSinceUpdate >= pt.fileInterval {
		pt.lastUpdate = now
		pt.lastFileCount = filesScanned
		return true
	}

	return false
}

// Scan scans all configured media paths and updates the database
func (s *Scanner) Scan(ctx context.Context, progressCb ProgressCallback) error {
	// In remote mode, check SFTP connection
	if !s.cfg.IsLocalMode() && s.sftpClient == nil {
		return fmt.Errorf("not connected - call Connect() first")
	}

	var totalProgress ScanProgress
	throttler := newProgressThrottler()
	scanStartTime := time.Now()

	mediaPaths := s.cfg.GetMediaPaths()

	// Pre-scan: count total files for progress percentage
	s.logDebug("Pre-scanning to count files...")
	for _, mediaPath := range mediaPaths {
		path := mediaPath
		if s.cfg.IsLocalMode() {
			path = expandPath(mediaPath)
		}
		fileCount, byteCount := s.countFilesInPath(ctx, path)
		totalProgress.TotalFiles += fileCount
		totalProgress.TotalBytes += byteCount
	}
	s.logDebug("Found %d files (%s total)", totalProgress.TotalFiles, formatBytes(totalProgress.TotalBytes))

	s.logDebug("Starting scan of %d media paths", len(mediaPaths))
	for _, mediaPath := range mediaPaths {
		path := mediaPath
		if s.cfg.IsLocalMode() {
			path = expandPath(mediaPath)
		}
		s.logDebug("Scanning path: %s", path)
		err := s.scanPath(ctx, path, &totalProgress, progressCb, throttler, scanStartTime)
		if err != nil {
			return fmt.Errorf("failed to scan %s: %w", path, err)
		}
	}

	// Send final progress update and flush logs
	s.sendProgress(&totalProgress, scanStartTime, progressCb)

	s.logDebug("Scan complete - %d files scanned, %d added, %d updated",
		totalProgress.FilesScanned, totalProgress.FilesAdded, totalProgress.FilesUpdated)

	// Flush logs one more time after final log message
	if s.logWriter != nil {
		_ = s.logWriter.Flush()
	}

	return nil
}

// calculateRates computes scan rates
func (s *Scanner) calculateRates(progress *ScanProgress, startTime time.Time) {
	elapsed := time.Since(startTime).Seconds()
	if elapsed > 0 {
		progress.FilesPerSec = float64(progress.FilesScanned) / elapsed
		progress.BytesPerSec = float64(progress.BytesScanned) / elapsed
	}
}

// sendProgress sends progress update and flushes logs
func (s *Scanner) sendProgress(progress *ScanProgress, startTime time.Time, progressCb ProgressCallback) {
	s.calculateRates(progress, startTime)
	if progressCb != nil {
		progressCb(*progress)
	}
	// Flush logs periodically with progress updates
	if s.logWriter != nil {
		_ = s.logWriter.Flush()
	}
}

// worker processes work items from the work channel using pooled SSH connections
func (s *Scanner) worker(ctx context.Context, workCh <-chan workItem, resultsCh chan<- workResult, wg *sync.WaitGroup) {
	defer wg.Done()

	for work := range workCh {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return
		default:
		}

		result := workResult{
			path:       work.path,
			size:       work.size,
			isNew:      work.existingFile == nil,
			existingID: 0,
		}

		if work.existingFile != nil {
			result.existingID = work.existingFile.ID
		}

		// Extract metadata using pooled SSH connection
		metadata, err := s.extractMetadata(ctx, work.path)
		if err != nil {
			result.err = err
			resultsCh <- result
			continue
		}

		result.metadata = metadata
		resultsCh <- result
	}
}

// scanPath recursively scans a single media path with parallel processing
func (s *Scanner) scanPath(ctx context.Context, path string, progress *ScanProgress, progressCb ProgressCallback, throttler *progressThrottler, startTime time.Time) error {
	// Create channels for work distribution
	numWorkers := s.cfg.Workers.MaxWorkers
	if numWorkers <= 0 {
		numWorkers = 4
	}

	workCh := make(chan workItem, numWorkers*2) // Buffered to avoid blocking directory walker
	resultsCh := make(chan workResult, numWorkers*2)

	// Start worker goroutines
	var workerWg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		workerWg.Add(1)
		go s.worker(ctx, workCh, resultsCh, &workerWg)
	}

	// Start results processor goroutine
	var resultsWg sync.WaitGroup
	resultsWg.Add(1)
	go s.processResults(ctx, resultsCh, progress, progressCb, throttler, startTime, &resultsWg)

	// Walk directory tree and send work items
	err := s.walkAndEnqueue(ctx, path, workCh, progress, progressCb, throttler, startTime)

	// Close work channel (workers will finish and exit)
	close(workCh)

	// Wait for all workers to finish
	workerWg.Wait()

	// Close results channel (results processor will finish)
	close(resultsCh)

	// Wait for results processor to finish
	resultsWg.Wait()

	return err
}

// processResults processes results from workers and batches database operations
func (s *Scanner) processResults(ctx context.Context, resultsCh <-chan workResult, progress *ScanProgress, progressCb ProgressCallback, throttler *progressThrottler, startTime time.Time, wg *sync.WaitGroup) {
	defer wg.Done()

	const batchSize = 20
	var newFiles []*types.MediaFile
	updatedFiles := make(map[int64]*types.MediaFile)

	flushBatches := func() {
		// Flush new files
		if len(newFiles) > 0 {
			s.logDebug("Batch inserting %d new files", len(newFiles))
			ids, err := s.db.AddMediaFileBatch(newFiles)
			if err != nil {
				s.logDebug("Batch insert failed: %v", err)
				s.progressMu.Lock()
				progress.ErrorCount += len(newFiles)
				progress.LastError = err
				s.progressMu.Unlock()
			} else {
				s.progressMu.Lock()
				progress.FilesAdded += len(ids)
				s.progressMu.Unlock()
			}
			newFiles = newFiles[:0]
		}

		// Flush updated files
		if len(updatedFiles) > 0 {
			s.logDebug("Batch updating %d files", len(updatedFiles))
			err := s.db.UpdateMediaFileBatch(updatedFiles)
			if err != nil {
				s.logDebug("Batch update failed: %v", err)
				s.progressMu.Lock()
				progress.ErrorCount += len(updatedFiles)
				progress.LastError = err
				s.progressMu.Unlock()
			} else {
				s.progressMu.Lock()
				progress.FilesUpdated += len(updatedFiles)
				s.progressMu.Unlock()
			}
			updatedFiles = make(map[int64]*types.MediaFile)
		}
	}

	for result := range resultsCh {
		if result.err != nil {
			s.progressMu.Lock()
			progress.ErrorCount++
			progress.LastError = fmt.Errorf("failed to process %s: %w", result.path, result.err)
			s.logDebug("Processing error: %v", result.err)
			s.progressMu.Unlock()
			continue
		}

		s.logDebug("Processed result for: %s (codec=%s)", result.path, result.metadata.Codec)

		if result.isNew {
			// Accumulate new files for batch insert
			newFiles = append(newFiles, result.metadata)
		} else {
			// Accumulate updated files for batch update
			result.metadata.ID = result.existingID
			updatedFiles[result.existingID] = result.metadata
		}

		// Flush batches when they reach the batch size
		if len(newFiles) >= batchSize || len(updatedFiles) >= batchSize {
			flushBatches()
		}

		// Update progress (throttled)
		s.progressMu.Lock()
		if throttler.shouldUpdate(progress.FilesScanned, false) {
			s.sendProgress(progress, startTime, progressCb)
		}
		s.progressMu.Unlock()
	}

	// Flush any remaining batches
	flushBatches()
}

// walkAndEnqueue walks the directory tree and enqueues work items
func (s *Scanner) walkAndEnqueue(ctx context.Context, path string, workCh chan<- workItem, progress *ScanProgress, progressCb ProgressCallback, throttler *progressThrottler, startTime time.Time) error {
	// Check for cancellation
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Update current path
	s.progressMu.Lock()
	progress.CurrentPath = path
	s.progress = *progress
	s.progressMu.Unlock()

	// List directory contents - use local or remote depending on mode
	var entries []os.FileInfo
	var err error

	if s.cfg.IsLocalMode() {
		// Local mode: use os.ReadDir
		dirEntries, readErr := os.ReadDir(path)
		if readErr != nil {
			err = readErr
		} else {
			entries = make([]os.FileInfo, 0, len(dirEntries))
			for _, de := range dirEntries {
				info, infoErr := de.Info()
				if infoErr == nil {
					entries = append(entries, info)
				}
			}
		}
	} else {
		// Remote mode: use SFTP
		entries, err = s.sftpClient.ReadDir(path)
	}

	if err != nil {
		s.progressMu.Lock()
		progress.ErrorCount++
		progress.LastError = fmt.Errorf("failed to read directory %s: %w", path, err)
		if throttler.shouldUpdate(progress.FilesScanned, true) {
			s.sendProgress(progress, startTime, progressCb)
		}
		s.progressMu.Unlock()
		return nil // Continue scanning other directories
	}

	for _, entry := range entries {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		fullPath := filepath.Join(path, entry.Name())

		if entry.IsDir() {
			// Recursively scan subdirectory
			if err := s.walkAndEnqueue(ctx, fullPath, workCh, progress, progressCb, throttler, startTime); err != nil {
				return err
			}
		} else if s.isVideoFile(entry.Name()) && s.shouldProcessFile(fullPath, entry.Size()) {
			// Update scan counters
			s.progressMu.Lock()
			progress.FilesScanned++
			progress.BytesScanned += entry.Size()
			s.progressMu.Unlock()

			fileSize := entry.Size()
			s.logDebug("Found video file: %s (%d bytes)", fullPath, fileSize)

			// Check if file exists in database
			existing, err := s.db.GetMediaFileByPath(fullPath)
			if err != nil {
				s.progressMu.Lock()
				progress.ErrorCount++
				progress.LastError = fmt.Errorf("database error for %s: %w", fullPath, err)
				if throttler.shouldUpdate(progress.FilesScanned, true) {
					s.sendProgress(progress, startTime, progressCb)
				}
				s.progressMu.Unlock()
				continue
			}

			// Fast path: existing file with matching size - skip ffprobe entirely
			if existing != nil && existing.FileSizeBytes == fileSize {
				// File unchanged - no work needed
				continue
			}

			// Enqueue work for parallel processing
			work := workItem{
				path:         fullPath,
				size:         fileSize,
				existingFile: existing,
			}

			select {
			case workCh <- work:
				s.logDebug("Enqueued work for: %s", fullPath)
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	return nil
}

// countFilesInPath recursively counts video files in a path (lightweight pre-scan)
func (s *Scanner) countFilesInPath(ctx context.Context, path string) (int, int64) {
	// Check for cancellation
	select {
	case <-ctx.Done():
		return 0, 0
	default:
	}

	var fileCount int
	var byteCount int64

	// List directory contents - use local or remote depending on mode
	var entries []os.FileInfo
	var err error

	if s.cfg.IsLocalMode() {
		// Local mode: use os.ReadDir
		dirEntries, readErr := os.ReadDir(path)
		if readErr != nil {
			err = readErr
		} else {
			entries = make([]os.FileInfo, 0, len(dirEntries))
			for _, de := range dirEntries {
				info, infoErr := de.Info()
				if infoErr == nil {
					entries = append(entries, info)
				}
			}
		}
	} else {
		// Remote mode: use SFTP
		entries, err = s.sftpClient.ReadDir(path)
	}

	if err != nil {
		return 0, 0 // Skip directories we can't read
	}

	for _, entry := range entries {
		fullPath := filepath.Join(path, entry.Name())

		if entry.IsDir() {
			// Recursively count files in subdirectory
			subFiles, subBytes := s.countFilesInPath(ctx, fullPath)
			fileCount += subFiles
			byteCount += subBytes
		} else {
			// Check if this is a video file we should process
			if s.isVideoFile(entry.Name()) && s.shouldProcessFile(fullPath, entry.Size()) {
				fileCount++
				byteCount += entry.Size()
			}
		}
	}

	return fileCount, byteCount
}

// BackfillChecksums calculates and updates checksums for files that are missing them
// This should be run as a background task after the main scan completes
func (s *Scanner) BackfillChecksums(ctx context.Context, progressCb ProgressCallback) error {
	if s.sftpClient == nil {
		return fmt.Errorf("not connected - call Connect() first")
	}

	// Get all files without checksums
	filesNeedingChecksums, err := s.db.GetFilesNeedingChecksums(100) // Process in batches
	if err != nil {
		return fmt.Errorf("failed to query files needing checksums: %w", err)
	}

	s.logDebug("Backfilling checksums for %d files", len(filesNeedingChecksums))

	var progress ScanProgress
	for _, file := range filesNeedingChecksums {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		progress.CurrentPath = file.FilePath
		progress.FilesScanned++
		s.progress = progress

		// Calculate checksum
		checksum, err := s.CalculateRemoteChecksum(file.FilePath)
		if err != nil {
			s.logDebug("Warning: failed to calculate checksum for %s: %v", file.FilePath, err)
			progress.ErrorCount++
			progress.LastError = err
		} else {
			// Update database
			if err := s.db.UpdateMediaFileChecksum(file.ID, checksum, string(s.checksumAlgo)); err != nil {
				s.logDebug("Warning: failed to update checksum for %s: %v", file.FilePath, err)
				progress.ErrorCount++
				progress.LastError = err
			} else {
				progress.FilesUpdated++
				s.logDebug("Backfilled checksum for %s", file.FilePath)
			}
		}

		if progressCb != nil {
			progressCb(progress)
		}
	}

	s.logDebug("Checksum backfill complete - %d files updated, %d errors",
		progress.FilesUpdated, progress.ErrorCount)
	return nil
}

// isVideoFile checks if a file has a video extension
func (s *Scanner) isVideoFile(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	// Remove leading dot from extension for comparison
	if len(ext) > 0 && ext[0] == '.' {
		ext = ext[1:]
	}

	// Use configured extensions
	for _, configExt := range s.cfg.Files.Extensions {
		// Normalize config extension (remove leading dot if present)
		normalizedExt := strings.ToLower(configExt)
		if len(normalizedExt) > 0 && normalizedExt[0] == '.' {
			normalizedExt = normalizedExt[1:]
		}
		if ext == normalizedExt {
			return true
		}
	}
	return false
}

// shouldProcessFile checks if a file should be processed based on size and exclude patterns
func (s *Scanner) shouldProcessFile(filePath string, fileSize int64) bool {
	// Check minimum file size
	if s.cfg.Files.MinSizeBytes > 0 && fileSize < s.cfg.Files.MinSizeBytes {
		return false
	}

	// Check exclude patterns
	for _, pattern := range s.cfg.Files.ExcludePatterns {
		if matched, _ := filepath.Match(pattern, filepath.Base(filePath)); matched {
			return false
		}
		// Also try matching against full path for directory patterns
		if matched, _ := filepath.Match(pattern, filePath); matched {
			return false
		}
	}

	return true
}

// extractMetadata extracts video metadata from a file using ffprobe
func (s *Scanner) extractMetadata(ctx context.Context, filePath string) (*types.MediaFile, error) {
	if s.cfg.IsLocalMode() {
		// Use local ffprobe
		return s.ExtractMetadataWithLocalFFprobe(ctx, filePath)
	}
	// Use remote FFprobe via SSH
	return s.ExtractMetadataWithFFprobe(ctx, filePath)
}

// DownloadFile downloads a remote file to a local path with progress tracking
func (s *Scanner) DownloadFile(ctx context.Context, remotePath, localPath string, progressCb func(bytesRead int64, totalBytes int64)) error {
	_, err := s.DownloadFileWithChecksum(ctx, remotePath, localPath, progressCb)
	return err
}

// DownloadFileWithChecksum downloads a remote file and returns its checksum
func (s *Scanner) DownloadFileWithChecksum(ctx context.Context, remotePath, localPath string, progressCb func(bytesRead int64, totalBytes int64)) (string, error) {
	if s.sftpClient == nil {
		return "", fmt.Errorf("not connected - call Connect() first")
	}

	// Open remote file
	remoteFile, err := s.sftpClient.Open(remotePath)
	if err != nil {
		return "", fmt.Errorf("failed to open remote file: %w", err)
	}
	defer func() { _ = remoteFile.Close() }()

	// Get file size
	stat, err := remoteFile.Stat()
	if err != nil {
		return "", fmt.Errorf("failed to stat remote file: %w", err)
	}
	totalBytes := stat.Size()

	// Create local file
	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return "", fmt.Errorf("failed to create local directory: %w", err)
	}

	localFile, err := os.Create(localPath)
	if err != nil {
		return "", fmt.Errorf("failed to create local file: %w", err)
	}
	defer func() { _ = localFile.Close() }()

	// Create hasher for checksum calculation
	hasher := xxhash.New()

	// Copy with progress tracking and checksum calculation
	buf := make([]byte, 32*1024) // 32KB buffer
	var bytesRead int64

	for {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		n, err := remoteFile.Read(buf)
		if n > 0 {
			// Write to local file
			if _, writeErr := localFile.Write(buf[:n]); writeErr != nil {
				return "", fmt.Errorf("failed to write to local file: %w", writeErr)
			}
			// Write to hasher for checksum calculation
			_, _ = hasher.Write(buf[:n])
			bytesRead += int64(n)

			// Report progress
			if progressCb != nil {
				progressCb(bytesRead, totalBytes)
			}
		}

		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("failed to read from remote file: %w", err)
		}
	}

	// Return the checksum
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// UploadFile uploads a local file to a remote path with progress tracking
func (s *Scanner) UploadFile(ctx context.Context, localPath, remotePath string, progressCb func(bytesWritten int64, totalBytes int64)) error {
	_, err := s.UploadFileWithChecksum(ctx, localPath, remotePath, progressCb)
	return err
}

// UploadFileWithChecksum uploads a local file and returns its checksum
func (s *Scanner) UploadFileWithChecksum(ctx context.Context, localPath, remotePath string, progressCb func(bytesWritten int64, totalBytes int64)) (string, error) {
	if s.sftpClient == nil {
		return "", fmt.Errorf("not connected - call Connect() first")
	}

	// Open local file
	localFile, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("failed to open local file: %w", err)
	}
	defer func() { _ = localFile.Close() }()

	// Get file size
	stat, err := localFile.Stat()
	if err != nil {
		return "", fmt.Errorf("failed to stat local file: %w", err)
	}
	totalBytes := stat.Size()

	// Create remote directory if needed
	remoteDir := filepath.Dir(remotePath)
	if err := s.sftpClient.MkdirAll(remoteDir); err != nil {
		return "", fmt.Errorf("failed to create remote directory: %w", err)
	}

	// Create remote file
	remoteFile, err := s.sftpClient.Create(remotePath)
	if err != nil {
		return "", fmt.Errorf("failed to create remote file: %w", err)
	}
	defer func() { _ = remoteFile.Close() }()

	// Create hasher for checksum calculation
	hasher := xxhash.New()

	// Copy with progress tracking and checksum calculation
	buf := make([]byte, 32*1024) // 32KB buffer
	var bytesWritten int64

	for {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		n, err := localFile.Read(buf)
		if n > 0 {
			// Write to remote file
			if _, writeErr := remoteFile.Write(buf[:n]); writeErr != nil {
				return "", fmt.Errorf("failed to write to remote file: %w", writeErr)
			}
			// Write to hasher for checksum calculation
			_, _ = hasher.Write(buf[:n])
			bytesWritten += int64(n)

			// Report progress
			if progressCb != nil {
				progressCb(bytesWritten, totalBytes)
			}
		}

		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("failed to read from local file: %w", err)
		}
	}

	// Return the checksum
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// DeleteRemoteFile deletes a file on the remote server
func (s *Scanner) DeleteRemoteFile(remotePath string) error {
	if s.sftpClient == nil {
		return fmt.Errorf("not connected - call Connect() first")
	}

	if err := s.sftpClient.Remove(remotePath); err != nil {
		return fmt.Errorf("failed to delete remote file: %w", err)
	}

	return nil
}

// RenameRemoteFile renames/moves a file on the remote server (atomic operation)
func (s *Scanner) RenameRemoteFile(oldPath, newPath string) error {
	if s.sftpClient == nil {
		return fmt.Errorf("not connected - call Connect() first")
	}

	s.logDebug("Renaming remote file: %s -> %s", oldPath, newPath)

	if err := s.sftpClient.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("failed to rename remote file: %w", err)
	}

	s.logDebug("Successfully renamed file")
	return nil
}

// CalculateRemoteChecksum calculates the checksum of a remote file via SSH
func (s *Scanner) CalculateRemoteChecksum(remotePath string) (string, error) {
	if s.sshClient == nil {
		return "", fmt.Errorf("not connected - call Connect() first")
	}

	session, err := s.sshClient.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer func() { _ = session.Close() }()

	// Use detected algorithm
	cmd := checksum.RemoteCommand(remotePath, s.checksumAlgo)
	s.logDebug("Calculating remote checksum: %s", cmd)

	output, err := session.Output(cmd)
	if err != nil {
		return "", fmt.Errorf("failed to calculate remote checksum: %w", err)
	}

	hash := strings.TrimSpace(string(output))
	s.logDebug("Remote checksum: %s", hash)
	return hash, nil
}

// GetProgress returns the current scan progress
func (s *Scanner) GetProgress() ScanProgress {
	return s.progress
}

// logDebug writes a debug message to the log file
func (s *Scanner) logDebug(format string, args ...interface{}) {
	if s.logWriter == nil {
		return
	}

	timestamp := time.Now().Format("2006-01-02 15:04:05")
	message := fmt.Sprintf(format, args...)
	logLine := fmt.Sprintf("[%s] DEBUG: %s\n", timestamp, message)

	_, _ = s.logWriter.WriteString(logLine)
}

// formatBytes formats bytes into a human-readable string
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

// expandPath expands ~ to home directory
func expandPath(path string) string {
	if len(path) > 0 && path[0] == '~' {
		home, err := os.UserHomeDir()
		if err == nil {
			if len(path) == 1 {
				return home
			}
			return filepath.Join(home, path[1:])
		}
	}
	return path
}
