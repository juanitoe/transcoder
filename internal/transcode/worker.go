package transcode

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"transcoder/internal/checksum"
	"transcoder/internal/config"
	"transcoder/internal/database"
	"transcoder/internal/logging"
	"transcoder/internal/scanner"
	"transcoder/internal/types"
)

// WorkerPool manages multiple transcoding workers
type WorkerPool struct {
	cfg            *config.Config
	db             *database.DB
	scanner        *scanner.Scanner
	encoder        *Encoder
	workers        []*Worker
	progressChan   chan types.ProgressUpdate
	mu             sync.Mutex
	ctx            context.Context
	cancel         context.CancelFunc
	workerCount    int
	pauseRequests  map[int64]bool // Job IDs to pause
	cancelRequests map[int64]bool // Job IDs to cancel
}

// NewWorkerPool creates a new worker pool
func NewWorkerPool(cfg *config.Config, db *database.DB, scanner *scanner.Scanner) *WorkerPool {
	ctx, cancel := context.WithCancel(context.Background())

	return &WorkerPool{
		cfg:            cfg,
		db:             db,
		scanner:        scanner,
		encoder:        New(cfg),
		workers:        make([]*Worker, 0),
		progressChan:   make(chan types.ProgressUpdate, 100),
		ctx:            ctx,
		cancel:         cancel,
		workerCount:    cfg.Workers.MaxWorkers,
		pauseRequests:  make(map[int64]bool),
		cancelRequests: make(map[int64]bool),
	}
}

// Start starts the worker pool
func (wp *WorkerPool) Start() {
	wp.mu.Lock()
	defer wp.mu.Unlock()

	// Create workers
	for i := 0; i < wp.workerCount; i++ {
		worker := NewWorker(i, wp.cfg, wp.db, wp.scanner, wp.encoder, wp.progressChan)
		wp.workers = append(wp.workers, worker)
		go worker.Run(wp.ctx, wp.pauseRequests, wp.cancelRequests)
	}
}

// Stop stops the worker pool gracefully
func (wp *WorkerPool) Stop() {
	wp.cancel()

	// Wait for all workers to finish
	wp.mu.Lock()
	defer wp.mu.Unlock()

	for _, worker := range wp.workers {
		worker.Wait()
	}

	close(wp.progressChan)
}

// GetProgressChan returns the progress update channel
func (wp *WorkerPool) GetProgressChan() <-chan types.ProgressUpdate {
	return wp.progressChan
}

// ScaleWorkers adjusts the number of workers
func (wp *WorkerPool) ScaleWorkers(newCount int) {
	wp.mu.Lock()
	defer wp.mu.Unlock()

	currentCount := len(wp.workers)

	if newCount > currentCount {
		// Add workers
		for i := currentCount; i < newCount; i++ {
			worker := NewWorker(i, wp.cfg, wp.db, wp.scanner, wp.encoder, wp.progressChan)
			wp.workers = append(wp.workers, worker)
			go worker.Run(wp.ctx, wp.pauseRequests, wp.cancelRequests)
		}
		logging.Info("Scaled workers up: %d -> %d", currentCount, newCount)
	} else if newCount < currentCount {
		// Signal excess workers to stop after their current job completes
		for i := newCount; i < currentCount; i++ {
			wp.workers[i].stopping = true
		}
		// Keep them in the slice until they actually stop
		// They'll exit after finishing their current job
		logging.Info("Scaled workers down: %d -> %d (workers will stop after current job)", currentCount, newCount)
	}

	wp.workerCount = newCount
	wp.cfg.Workers.MaxWorkers = newCount
}

// PauseJob requests a job to be paused
func (wp *WorkerPool) PauseJob(jobID int64) {
	wp.mu.Lock()
	defer wp.mu.Unlock()
	wp.pauseRequests[jobID] = true
}

// CancelJob requests a job to be canceled
func (wp *WorkerPool) CancelJob(jobID int64) {
	wp.mu.Lock()
	defer wp.mu.Unlock()
	wp.cancelRequests[jobID] = true
}

// Worker represents a single transcoding worker
type Worker struct {
	id           int
	cfg          *config.Config
	db           *database.DB
	scanner      *scanner.Scanner
	encoder      *Encoder
	progressChan chan types.ProgressUpdate
	wg           sync.WaitGroup
	stopping     bool // Signal to stop after current job completes
}

// NewWorker creates a new worker
func NewWorker(id int, cfg *config.Config, db *database.DB, scanner *scanner.Scanner, encoder *Encoder, progressChan chan types.ProgressUpdate) *Worker {
	return &Worker{
		id:           id,
		cfg:          cfg,
		db:           db,
		scanner:      scanner,
		encoder:      encoder,
		progressChan: progressChan,
	}
}

// Run runs the worker loop
func (w *Worker) Run(ctx context.Context, pauseRequests, cancelRequests map[int64]bool) {
	w.wg.Add(1)
	defer w.wg.Done()

	workerID := fmt.Sprintf("worker-%d", w.id)

	for {
		// Check if context is cancelled
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Check if this worker should stop (graceful shutdown after scaling down)
		if w.stopping {
			return
		}

		// Claim next job from queue
		job, err := w.db.ClaimNextJob(workerID)
		if err != nil {
			// Log error and retry
			logging.Error("[%s] Failed to claim job: %v", workerID, err)
			time.Sleep(5 * time.Second)
			continue
		}

		if job == nil {
			// No jobs available, wait and retry
			time.Sleep(2 * time.Second)
			continue
		}

		// Log job claimed
		logging.Info("[%s] Claimed job %d: %s", workerID, job.ID, job.FileName)

		// Process the job (will complete fully before checking stopping flag again)
		w.processJob(ctx, job, pauseRequests, cancelRequests)
	}
}

// Wait waits for the worker to finish
func (w *Worker) Wait() {
	w.wg.Wait()
}

// processJob processes a single transcode job
func (w *Worker) processJob(ctx context.Context, job *types.TranscodeJob, pauseRequests, cancelRequests map[int64]bool) {
	startTime := time.Now()
	workerID := fmt.Sprintf("worker-%d", w.id)

	// Create job-specific context
	jobCtx, jobCancel := context.WithCancel(ctx)
	defer jobCancel()

	// In local mode, we don't need SSH/SFTP connections
	var jobScanner *scanner.Scanner
	if !w.cfg.IsLocalMode() {
		// Create a dedicated scanner with its own SSH connection for this job
		var err error
		jobScanner, err = scanner.New(w.cfg, w.db)
		if err != nil {
			logging.Error("[%s] Job %d failed: Failed to create scanner: %v", workerID, job.ID, err)
			_ = w.db.FailJob(job.ID, fmt.Sprintf("Failed to create scanner: %v", err))
			return
		}

		// Establish SSH/SFTP connection
		if err := jobScanner.Connect(jobCtx); err != nil {
			logging.Error("[%s] Job %d failed: Failed to connect to remote server: %v", workerID, job.ID, err)
			_ = w.db.FailJob(job.ID, fmt.Sprintf("Failed to connect to remote server: %v", err))
			return
		}
		defer func() { _ = jobScanner.Close() }()
	}

	// Monitor for pause/cancel requests
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-jobCtx.Done():
				return
			case <-ticker.C:
				// Check for cancel request
				if cancelRequests[job.ID] {
					delete(cancelRequests, job.ID)
					_ = w.db.CancelJob(job.ID)
					jobCancel()
					return
				}

				// Check for pause request
				if pauseRequests[job.ID] {
					delete(pauseRequests, job.ID)
					_ = w.db.PauseJob(job.ID)
					jobCancel()
					return
				}
			}
		}
	}()

	// Work directory for this job
	workDir := filepath.Join(expandPath(w.cfg.Workers.WorkDir), fmt.Sprintf("job-%d", job.ID))

	// Create work directory if it doesn't exist
	if err := os.MkdirAll(workDir, 0755); err != nil {
		logging.Error("[%s] Job %d failed: Failed to create work directory: %v", workerID, job.ID, err)
		_ = w.db.FailJob(job.ID, fmt.Sprintf("Failed to create work directory: %v", err))
		return
	}

	// Track job completion status for cleanup
	jobCompleted := false
	defer func() {
		// Clean up work directory if job failed or was canceled
		if !jobCompleted {
			_ = os.RemoveAll(workDir)
		}
	}()

	// In local mode, input path is the original file; in remote mode, download to work dir
	var localInputPath string
	var localOutputPath string

	if w.cfg.IsLocalMode() {
		// Local mode: input is the original file, output goes to work dir
		localInputPath = job.FilePath
		localOutputPath = filepath.Join(workDir, "transcoded_"+job.FileName)
	} else {
		// Remote mode: download to work dir
		localInputPath = filepath.Join(workDir, job.FileName)
		localOutputPath = filepath.Join(workDir, "transcoded_"+job.FileName)
	}

	// Track checksums
	var localInputChecksum string

	// Stage 1: Download (skip in local mode, or if file already exists - recovered job)
	if w.cfg.IsLocalMode() {
		// Local mode: no download needed, just calculate checksum
		w.updateProgress(job.ID, types.StageDownload, 100, "Local mode - no download needed", job.FileSizeBytes, job.FileSizeBytes)
		result, err := checksum.CalculateFile(localInputPath)
		if err == nil {
			localInputChecksum = result.Hash
		}
	} else if _, err := os.Stat(localInputPath); os.IsNotExist(err) {
		_ = w.db.UpdateJobStatus(job.ID, types.StatusDownloading, types.StageDownload, 0)
		w.updateProgress(job.ID, types.StageDownload, 0, "Downloading", 0, job.FileSizeBytes)
		localInputChecksum, err = jobScanner.DownloadFileWithChecksum(jobCtx, job.FilePath, localInputPath, func(bytesRead, totalBytes int64) {
			progress := (float64(bytesRead) / float64(totalBytes)) * 100
			w.updateProgress(job.ID, types.StageDownload, progress, fmt.Sprintf("Downloading: %.1f%%", progress), bytesRead, totalBytes)
		})

		if err != nil {
			logging.Error("[%s] Job %d failed: Download failed: %v", workerID, job.ID, err)
			_ = w.db.FailJob(job.ID, fmt.Sprintf("Download failed: %v", err))
			return
		}
	} else {
		// File already exists (recovered job), calculate checksum
		w.updateProgress(job.ID, types.StageDownload, 100, "Download skipped (file already exists)", job.FileSizeBytes, job.FileSizeBytes)
		result, err := checksum.CalculateFile(localInputPath)
		if err == nil {
			localInputChecksum = result.Hash
		}
	}

	// Check if cancelled
	select {
	case <-jobCtx.Done():
		return
	default:
	}

	// Stage 2: Transcode (skip if output already exists and is valid - recovered job)
	var localOutputChecksum string
	if _, err := os.Stat(localOutputPath); err == nil {
		// Transcoded file exists - verify it's valid AND has correct duration
		mediaFile, _ := w.db.GetMediaFileByPath(job.FilePath)
		var expectedDuration float64
		if mediaFile != nil {
			expectedDuration = mediaFile.DurationSeconds
		}

		// Verify duration matches (prevents uploading truncated files)
		if expectedDuration > 0 {
			if err := w.encoder.VerifyDuration(jobCtx, localOutputPath, expectedDuration); err == nil {
				// Valid transcoded file with correct duration - skip to upload
				w.updateProgress(job.ID, types.StageTranscode, 100, "Transcode skipped (file already exists)", 0, 0)
				result, err := checksum.CalculateFile(localOutputPath)
				if err != nil {
					logging.Error("[%s] Job %d failed: Failed to calculate output checksum: %v", workerID, job.ID, err)
					_ = w.db.FailJob(job.ID, fmt.Sprintf("Failed to calculate output checksum: %v", err))
					return
				}
				localOutputChecksum = result.Hash
			} else {
				// Invalid or truncated file - remove and re-transcode
				_ = os.Remove(localOutputPath)
			}
		} else {
			// No expected duration - fall back to basic verify
			if err := w.encoder.Verify(jobCtx, localOutputPath); err == nil {
				w.updateProgress(job.ID, types.StageTranscode, 100, "Transcode skipped (file already exists)", 0, 0)
				result, err := checksum.CalculateFile(localOutputPath)
				if err != nil {
					logging.Error("[%s] Job %d failed: Failed to calculate output checksum: %v", workerID, job.ID, err)
					_ = w.db.FailJob(job.ID, fmt.Sprintf("Failed to calculate output checksum: %v", err))
					return
				}
				localOutputChecksum = result.Hash
			} else {
				_ = os.Remove(localOutputPath)
			}
		}
	}

	// Only transcode if we don't have a valid output yet
	if localOutputChecksum == "" {
		// Pre-transcode check: estimate if transcoding is worthwhile
		var mediaFile *types.MediaFile
		mediaFile, _ = w.db.GetMediaFileByPath(job.FilePath)
		if mediaFile != nil {
			logging.Debug("[%s] Job %d pre-check: codec=%s bitrate=%d kbps resolution=%dx%d",
				workerID, job.ID, mediaFile.Codec, mediaFile.BitrateKbps,
				mediaFile.ResolutionWidth, mediaFile.ResolutionHeight)

			estimatedSavings, shouldProceed, reason := EstimateTranscodingSavings(
				mediaFile.Codec,
				mediaFile.BitrateKbps,
				mediaFile.ResolutionWidth,
				mediaFile.ResolutionHeight,
				w.cfg.Encoder.Quality,
			)

			minSavings := w.cfg.Encoder.MinExpectedSavings
			if !shouldProceed || estimatedSavings < minSavings {
				skipReason := fmt.Sprintf("Skipped: %s (expected %d%% < min %d%%)", reason, estimatedSavings, minSavings)
				logging.Info("[%s] Job %d skipped pre-transcode: %s", workerID, job.ID, skipReason)
				_ = w.db.SkipJob(job.ID, skipReason, 0)
				// Clean up downloaded file
				_ = os.Remove(localInputPath)
				return
			}
			logging.Info("[%s] Job %d proceeding: %s", workerID, job.ID, reason)
		} else {
			logging.Warn("[%s] Job %d: Could not get media file info for pre-transcode check, proceeding anyway", workerID, job.ID)
		}

		_ = w.db.UpdateJobStatus(job.ID, types.StatusTranscoding, types.StageTranscode, 0)
		w.updateProgress(job.ID, types.StageTranscode, 0, "Transcoding", 0, 0)

		// Start a goroutine to periodically update file size and check for early termination
		fileSizeDone := make(chan bool)
		sizeExceeded := make(chan int64, 1) // Buffered to avoid blocking
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()

			for {
				select {
				case <-fileSizeDone:
					return
				case <-ticker.C:
					// Check output file size
					if fileInfo, err := os.Stat(localOutputPath); err == nil {
						currentSize := fileInfo.Size()
						_ = w.db.UpdateJobFileSize(job.ID, currentSize)

						// Early termination: if output already exceeds original, stop transcoding
						if !w.cfg.Encoder.AllowLargerOutput && currentSize > job.FileSizeBytes {
							select {
							case sizeExceeded <- currentSize:
								jobCancel() // Cancel the transcode
							default:
								// Already sent, don't block
							}
							return
						}
					}
				}
			}
		}()

		// Get duration for progress calculation (in microseconds)
		var durationUs int64 = 1
		// Reuse mediaFile from pre-transcode check if available, otherwise fetch it
		if mediaFile == nil {
			mediaFile, _ = w.db.GetMediaFileByPath(job.FilePath)
		}
		if mediaFile != nil && mediaFile.DurationSeconds > 0 {
			durationUs = int64(mediaFile.DurationSeconds * 1000000)
		}

		err := w.encoder.Transcode(jobCtx, localInputPath, localOutputPath, durationUs, func(progress TranscodeProgress) {
			w.updateProgress(job.ID, types.StageTranscode, progress.Progress,
				fmt.Sprintf("Transcoding: frame=%d fps=%.1f speed=%.2fx", progress.Frame, progress.FPS, progress.Speed), 0, 0)
		})

		close(fileSizeDone)

		// Check if cancelled due to size exceeded (early termination)
		select {
		case exceededSize := <-sizeExceeded:
			sizeIncrease := float64(exceededSize-job.FileSizeBytes) / float64(job.FileSizeBytes) * 100
			reason := fmt.Sprintf("Transcoded file exceeded original during encoding (%.1f%% larger at %d bytes, original %d bytes)",
				sizeIncrease, exceededSize, job.FileSizeBytes)
			logging.Info("[%s] Job %d skipped (early termination): %s", workerID, job.ID, reason)
			_ = w.db.SkipJob(job.ID, reason, exceededSize)
			// Clean up partial output file
			_ = os.Remove(localOutputPath)
			return
		default:
		}

		if err != nil {
			logging.Error("[%s] Job %d failed: Transcode failed: %v", workerID, job.ID, err)
			_ = w.db.FailJob(job.ID, fmt.Sprintf("Transcode failed: %v", err))
			return
		}

		// Check if cancelled (pause/cancel request)
		select {
		case <-jobCtx.Done():
			return
		default:
		}

		// Stage 3: Validate
		w.updateProgress(job.ID, types.StageValidate, 0, "Validating", 0, 0)
		if err := w.encoder.Verify(jobCtx, localOutputPath); err != nil {
			logging.Error("[%s] Job %d failed: Validation failed: %v", workerID, job.ID, err)
			_ = w.db.FailJob(job.ID, fmt.Sprintf("Validation failed: %v", err))
			return
		}

		// Calculate checksum of transcoded file
		result, err := checksum.CalculateFile(localOutputPath)
		if err != nil {
			logging.Error("[%s] Job %d failed: Failed to calculate output checksum: %v", workerID, job.ID, err)
			_ = w.db.FailJob(job.ID, fmt.Sprintf("Failed to calculate output checksum: %v", err))
			return
		}
		localOutputChecksum = result.Hash
		w.updateProgress(job.ID, types.StageValidate, 100, "Validation passed", 0, 0)
	}

	// Check if cancelled
	select {
	case <-jobCtx.Done():
		return
	default:
	}

	// Get transcoded file size
	transcodedInfo, err := w.encoder.GetFileInfo(localOutputPath)
	if err != nil {
		logging.Error("[%s] Job %d failed: Failed to get transcoded file info: %v", workerID, job.ID, err)
		_ = w.db.FailJob(job.ID, fmt.Sprintf("Failed to get transcoded file info: %v", err))
		return
	}

	// Check if transcoded file is larger than original
	if !w.cfg.Encoder.AllowLargerOutput && transcodedInfo.Size >= job.FileSizeBytes {
		sizeIncrease := float64(transcodedInfo.Size-job.FileSizeBytes) / float64(job.FileSizeBytes) * 100
		reason := fmt.Sprintf("Transcoded file is larger than original (%.1f%% increase: %d -> %d bytes)",
			sizeIncrease, job.FileSizeBytes, transcodedInfo.Size)
		logging.Info("[%s] Job %d skipped: %s", workerID, job.ID, reason)
		_ = w.db.SkipJob(job.ID, reason, transcodedInfo.Size)
		// Clean up local transcoded file
		_ = os.Remove(localOutputPath)
		return
	}

	// Stage 4: Upload / Replace file
	var uploadedChecksum string

	if w.cfg.IsLocalMode() {
		// Local mode: replace file directly without upload
		_ = w.db.UpdateJobStatus(job.ID, types.StatusUploading, types.StageUpload, 0)
		w.updateProgress(job.ID, types.StageUpload, 0, "Replacing original file", 0, transcodedInfo.Size)

		// Replace original file with transcoded file
		if err := w.replaceLocalFile(job.FilePath, localOutputPath, w.cfg.Files.KeepOriginal); err != nil {
			logging.Error("[%s] Job %d failed: Failed to replace original file: %v", workerID, job.ID, err)
			_ = w.db.FailJob(job.ID, fmt.Sprintf("Failed to replace original file: %v", err))
			_ = os.Remove(localOutputPath) // Clean up transcoded file
			return
		}

		// In local mode, the "uploaded" checksum is the same as output checksum
		uploadedChecksum = localOutputChecksum
		w.updateProgress(job.ID, types.StageUpload, 100, "File replaced successfully", transcodedInfo.Size, transcodedInfo.Size)
	} else {
		// Remote mode: upload via SFTP
		_ = w.db.UpdateJobStatus(job.ID, types.StatusUploading, types.StageUpload, 0)
		w.updateProgress(job.ID, types.StageUpload, 0, "Uploading", 0, transcodedInfo.Size)

		// Upload to temporary location first
		remoteTempPath := job.FilePath + ".transcoded"
		uploadedChecksum, err = jobScanner.UploadFileWithChecksum(jobCtx, localOutputPath, remoteTempPath, func(bytesWritten, totalBytes int64) {
			progress := (float64(bytesWritten) / float64(totalBytes)) * 100
			w.updateProgress(job.ID, types.StageUpload, progress, fmt.Sprintf("Uploading: %.1f%%", progress), bytesWritten, totalBytes)
		})

		if err != nil {
			logging.Error("[%s] Job %d failed: Upload failed: %v", workerID, job.ID, err)
			_ = w.db.FailJob(job.ID, fmt.Sprintf("Upload failed: %v", err))
			return
		}

		// Verify upload checksum matches local output checksum
		if uploadedChecksum != localOutputChecksum {
			if w.cfg.Workers.SkipChecksumVerification {
				// Log warning but continue (skip verification)
				logging.Warn("[%s] Job %d: Upload checksum mismatch (verification skipped): expected %s, got %s", workerID, job.ID, localOutputChecksum, uploadedChecksum)
			} else {
				// Fail job on checksum mismatch
				logging.Error("[%s] Job %d failed: Upload checksum mismatch: expected %s, got %s", workerID, job.ID, localOutputChecksum, uploadedChecksum)
				_ = w.db.FailJob(job.ID, fmt.Sprintf("Upload checksum mismatch: expected %s, got %s", localOutputChecksum, uploadedChecksum))
				_ = jobScanner.DeleteRemoteFile(remoteTempPath)
				return
			}
		}

		// Check if cancelled
		select {
		case <-jobCtx.Done():
			// Remove temp file if cancelled after upload
			_ = jobScanner.DeleteRemoteFile(remoteTempPath)
			return
		default:
		}

		// Replace original file with transcoded file atomically
		if w.cfg.Files.KeepOriginal {
			// Keep original file by renaming it to .original
			originalBackupPath := job.FilePath + ".original"
			if err := jobScanner.RenameRemoteFile(job.FilePath, originalBackupPath); err != nil {
				logging.Error("[%s] Job %d failed: Failed to backup original file: %v", workerID, job.ID, err)
				_ = w.db.FailJob(job.ID, fmt.Sprintf("Failed to backup original file: %v", err))
				_ = jobScanner.DeleteRemoteFile(remoteTempPath) // Clean up temp file
				return
			}

			// Rename transcoded file to original name (atomic operation)
			if err := jobScanner.RenameRemoteFile(remoteTempPath, job.FilePath); err != nil {
				logging.Error("[%s] Job %d failed: Failed to rename transcoded file: %v", workerID, job.ID, err)
				_ = w.db.FailJob(job.ID, fmt.Sprintf("Failed to rename transcoded file: %v", err))
				// Try to restore original from backup
				_ = jobScanner.RenameRemoteFile(originalBackupPath, job.FilePath)
				_ = jobScanner.DeleteRemoteFile(remoteTempPath) // Clean up temp file
				return
			}

			logging.Info("[%s] Job %d: Original file kept as backup: %s", workerID, job.ID, originalBackupPath)
		} else {
			// Delete original file and replace with transcoded
			if err := jobScanner.DeleteRemoteFile(job.FilePath); err != nil {
				logging.Error("[%s] Job %d failed: Failed to delete original file: %v", workerID, job.ID, err)
				_ = w.db.FailJob(job.ID, fmt.Sprintf("Failed to delete original file: %v", err))
				_ = jobScanner.DeleteRemoteFile(remoteTempPath) // Clean up temp file
				return
			}

			// Rename transcoded file to original name (atomic operation)
			if err := jobScanner.RenameRemoteFile(remoteTempPath, job.FilePath); err != nil {
				logging.Error("[%s] Job %d failed: Failed to rename transcoded file: %v", workerID, job.ID, err)
				_ = w.db.FailJob(job.ID, fmt.Sprintf("Failed to rename transcoded file: %v", err))
				_ = jobScanner.DeleteRemoteFile(remoteTempPath) // Clean up temp file
				return
			}
		}
	}

	// Calculate encoding time and stats
	encodingTime := int(time.Since(startTime).Seconds())
	fps := 0.0 // We could calculate this from total frames / encoding time

	// Complete the job with checksum information
	_ = w.db.CompleteJobWithChecksums(job.ID, transcodedInfo.Size, encodingTime, fps, localInputChecksum, localOutputChecksum, uploadedChecksum)
	w.updateProgress(job.ID, types.StageUpload, 100, "Completed", transcodedInfo.Size, transcodedInfo.Size)

	// Log successful completion with stats
	sizeReduction := float64(job.FileSizeBytes-transcodedInfo.Size) / float64(job.FileSizeBytes) * 100
	logging.Info("[%s] Job %d completed: %s (%.1f%% size reduction, %ds encode time)",
		workerID, job.ID, job.FileName, sizeReduction, encodingTime)

	// Mark job as completed so cleanup doesn't delete work directory
	jobCompleted = true

	// Clean up work directory after successful completion
	_ = os.RemoveAll(workDir)
}

// updateProgress sends a progress update
func (w *Worker) updateProgress(jobID int64, stage types.ProcessingStage, progress float64, message string, bytesTransferred, totalBytes int64) {
	// Update database with stage
	_ = w.db.UpdateJobProgress(jobID, progress, 0.0, string(stage))

	// Send to progress channel
	select {
	case w.progressChan <- types.ProgressUpdate{
		JobID:            jobID,
		WorkerID:         fmt.Sprintf("worker-%d", w.id),
		Stage:            stage,
		Progress:         progress,
		Message:          message,
		BytesTransferred: bytesTransferred,
		TotalBytes:       totalBytes,
		Timestamp:        time.Now(),
	}:
	default:
		// Channel full, skip this update
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

// replaceLocalFile replaces the original file with the transcoded file
func (w *Worker) replaceLocalFile(originalPath, transcodedPath string, keepOriginal bool) error {
	if keepOriginal {
		// Rename original to .original backup
		backupPath := originalPath + ".original"
		if err := os.Rename(originalPath, backupPath); err != nil {
			return fmt.Errorf("failed to backup original file: %w", err)
		}
		logging.Info("Original file backed up to: %s", backupPath)
	} else {
		// Delete original file
		if err := os.Remove(originalPath); err != nil {
			return fmt.Errorf("failed to delete original file: %w", err)
		}
	}

	// Move transcoded file to original location
	if err := os.Rename(transcodedPath, originalPath); err != nil {
		// If rename fails (e.g., cross-device), try copy + delete
		if copyErr := copyFile(transcodedPath, originalPath); copyErr != nil {
			return fmt.Errorf("failed to move transcoded file: %w (copy also failed: %v)", err, copyErr)
		}
		_ = os.Remove(transcodedPath)
	}

	return nil
}

// copyFile copies a file from src to dst
func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = sourceFile.Close() }()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() { _ = destFile.Close() }()

	buf := make([]byte, 32*1024)
	for {
		n, readErr := sourceFile.Read(buf)
		if n > 0 {
			if _, writeErr := destFile.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return readErr
		}
	}

	return destFile.Sync()
}
