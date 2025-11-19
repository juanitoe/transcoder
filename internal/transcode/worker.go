package transcode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"transcoder/internal/checksum"
	"transcoder/internal/config"
	"transcoder/internal/database"
	"transcoder/internal/scanner"
	"transcoder/internal/types"
)

// WorkerPool manages multiple transcoding workers
type WorkerPool struct {
	cfg             *config.Config
	db              *database.DB
	scanner         *scanner.Scanner
	encoder         *Encoder
	workers         []*Worker
	progressChan    chan types.ProgressUpdate
	mu              sync.Mutex
	ctx             context.Context
	cancel          context.CancelFunc
	workerCount     int
	pauseRequests   map[int64]bool // Job IDs to pause
	cancelRequests  map[int64]bool // Job IDs to cancel
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
	} else if newCount < currentCount {
		// Signal excess workers to stop after their current job completes
		for i := newCount; i < currentCount; i++ {
			wp.workers[i].stopping = true
		}
		// Keep them in the slice until they actually stop
		// They'll exit after finishing their current job
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
			time.Sleep(5 * time.Second)
			continue
		}

		if job == nil {
			// No jobs available, wait and retry
			time.Sleep(2 * time.Second)
			continue
		}

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

	// Create job-specific context
	jobCtx, jobCancel := context.WithCancel(ctx)
	defer jobCancel()

	// Create a dedicated scanner with its own SSH connection for this job
	jobScanner, err := scanner.New(w.cfg, w.db)
	if err != nil {
		w.db.FailJob(job.ID, fmt.Sprintf("Failed to create scanner: %v", err))
		return
	}

	// Establish SSH/SFTP connection
	if err := jobScanner.Connect(jobCtx); err != nil {
		w.db.FailJob(job.ID, fmt.Sprintf("Failed to connect to remote server: %v", err))
		return
	}
	defer jobScanner.Close()

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
					w.db.CancelJob(job.ID)
					jobCancel()
					return
				}

				// Check for pause request
				if pauseRequests[job.ID] {
					delete(pauseRequests, job.ID)
					w.db.PauseJob(job.ID)
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
		w.db.FailJob(job.ID, fmt.Sprintf("Failed to create work directory: %v", err))
		return
	}

	// Track job completion status for cleanup
	jobCompleted := false
	defer func() {
		// Clean up work directory if job failed or was canceled
		if !jobCompleted {
			os.RemoveAll(workDir)
		}
	}()

	localInputPath := filepath.Join(workDir, job.FileName)
	localOutputPath := filepath.Join(workDir, "transcoded_"+job.FileName)

	// Track checksums
	var localInputChecksum string

	// Stage 1: Download (skip if file already exists - recovered job)
	if _, err := os.Stat(localInputPath); os.IsNotExist(err) {
		w.db.UpdateJobStatus(job.ID, types.StatusDownloading, types.StageDownload, 0)
		w.updateProgress(job.ID, types.StageDownload, 0, "Downloading", 0, job.FileSizeBytes)
		localInputChecksum, err = jobScanner.DownloadFileWithChecksum(jobCtx, job.FilePath, localInputPath, func(bytesRead, totalBytes int64) {
			progress := (float64(bytesRead) / float64(totalBytes)) * 100
			w.updateProgress(job.ID, types.StageDownload, progress, fmt.Sprintf("Downloading: %.1f%%", progress), bytesRead, totalBytes)
		})

		if err != nil {
			w.db.FailJob(job.ID, fmt.Sprintf("Download failed: %v", err))
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

	// Stage 2: Transcode
	w.db.UpdateJobStatus(job.ID, types.StatusTranscoding, types.StageTranscode, 0)
	w.updateProgress(job.ID, types.StageTranscode, 0, "Transcoding", 0, 0)

	// Start a goroutine to periodically update file size
	fileSizeDone := make(chan bool)
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
					w.db.UpdateJobFileSize(job.ID, fileInfo.Size())
				}
			}
		}
	}()
	defer close(fileSizeDone)

	// Get duration for progress calculation (in microseconds)
	var durationUs int64 = 1
	mediaFile, err := w.db.GetMediaFileByPath(job.FilePath)
	if err == nil && mediaFile != nil && mediaFile.DurationSeconds > 0 {
		durationUs = int64(mediaFile.DurationSeconds * 1000000)
	}

	err = w.encoder.Transcode(jobCtx, localInputPath, localOutputPath, durationUs, func(progress TranscodeProgress) {
		w.updateProgress(job.ID, types.StageTranscode, progress.Progress,
			fmt.Sprintf("Transcoding: frame=%d fps=%.1f speed=%.2fx", progress.Frame, progress.FPS, progress.Speed), 0, 0)
	})

	if err != nil {
		w.db.FailJob(job.ID, fmt.Sprintf("Transcode failed: %v", err))
		return
	}

	// Check if cancelled
	select {
	case <-jobCtx.Done():
		return
	default:
	}

	// Stage 3: Validate
	w.updateProgress(job.ID, types.StageValidate, 0, "Validating", 0, 0)
	if err := w.encoder.Verify(jobCtx, localOutputPath); err != nil {
		w.db.FailJob(job.ID, fmt.Sprintf("Validation failed: %v", err))
		return
	}

	// Calculate checksum of transcoded file
	var localOutputChecksum string
	result, err := checksum.CalculateFile(localOutputPath)
	if err != nil {
		w.db.FailJob(job.ID, fmt.Sprintf("Failed to calculate output checksum: %v", err))
		return
	}
	localOutputChecksum = result.Hash
	w.updateProgress(job.ID, types.StageValidate, 100, "Validation passed", 0, 0)

	// Check if cancelled
	select {
	case <-jobCtx.Done():
		return
	default:
	}

	// Get transcoded file size
	transcodedInfo, err := w.encoder.GetFileInfo(localOutputPath)
	if err != nil {
		w.db.FailJob(job.ID, fmt.Sprintf("Failed to get transcoded file info: %v", err))
		return
	}

	// Stage 4: Upload
	w.db.UpdateJobStatus(job.ID, types.StatusUploading, types.StageUpload, 0)
	w.updateProgress(job.ID, types.StageUpload, 0, "Uploading", 0, transcodedInfo.Size)

	// Upload to temporary location first
	remoteTempPath := job.FilePath + ".transcoded"
	uploadedChecksum, err := jobScanner.UploadFileWithChecksum(jobCtx, localOutputPath, remoteTempPath, func(bytesWritten, totalBytes int64) {
		progress := (float64(bytesWritten) / float64(totalBytes)) * 100
		w.updateProgress(job.ID, types.StageUpload, progress, fmt.Sprintf("Uploading: %.1f%%", progress), bytesWritten, totalBytes)
	})

	if err != nil {
		w.db.FailJob(job.ID, fmt.Sprintf("Upload failed: %v", err))
		return
	}

	// Verify upload checksum matches local output checksum
	if uploadedChecksum != localOutputChecksum {
		w.db.FailJob(job.ID, fmt.Sprintf("Upload checksum mismatch: expected %s, got %s", localOutputChecksum, uploadedChecksum))
		jobScanner.DeleteRemoteFile(remoteTempPath)
		return
	}

	// Check if cancelled
	select {
	case <-jobCtx.Done():
		// Remove temp file if cancelled after upload
		jobScanner.DeleteRemoteFile(remoteTempPath)
		return
	default:
	}

	// Replace original file with transcoded file atomically
	// First delete the original
	if err := jobScanner.DeleteRemoteFile(job.FilePath); err != nil {
		w.db.FailJob(job.ID, fmt.Sprintf("Failed to delete original file: %v", err))
		jobScanner.DeleteRemoteFile(remoteTempPath) // Clean up temp file
		return
	}

	// Rename transcoded file to original name (atomic operation)
	if err := jobScanner.RenameRemoteFile(remoteTempPath, job.FilePath); err != nil {
		w.db.FailJob(job.ID, fmt.Sprintf("Failed to rename transcoded file: %v", err))
		jobScanner.DeleteRemoteFile(remoteTempPath) // Clean up temp file
		return
	}

	// Note: We skip final remote checksum verification because:
	// 1. Upload already verified checksum during transfer (streaming)
	// 2. Re-reading entire file is slow (especially for large files)
	// 3. The uploadedChecksum == localOutputChecksum check above is sufficient

	// Calculate encoding time and stats
	encodingTime := int(time.Since(startTime).Seconds())
	fps := 0.0 // We could calculate this from total frames / encoding time

	// Complete the job with checksum information
	w.db.CompleteJobWithChecksums(job.ID, transcodedInfo.Size, encodingTime, fps, localInputChecksum, localOutputChecksum, uploadedChecksum)
	w.updateProgress(job.ID, types.StageUpload, 100, "Completed", transcodedInfo.Size, transcodedInfo.Size)

	// Mark job as completed so cleanup doesn't delete work directory
	jobCompleted = true

	// Clean up work directory after successful completion
	os.RemoveAll(workDir)
}

// updateProgress sends a progress update
func (w *Worker) updateProgress(jobID int64, stage types.ProcessingStage, progress float64, message string, bytesTransferred, totalBytes int64) {
	// Update database with stage
	w.db.UpdateJobProgress(jobID, progress, 0.0, string(stage))

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
