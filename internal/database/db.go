package database

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3" // CGO SQLite driver (more stable)

	"transcoder/internal/types"
)

// DB represents the database connection
type DB struct {
	conn *sql.DB
}

// New creates a new database connection and initializes the schema
func New(dbPath string) (*DB, error) {
	// Expand environment variables and home directory
	dbPath = os.ExpandEnv(dbPath)
	if dbPath[0] == '~' {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		if len(dbPath) == 1 {
			dbPath = home
		} else {
			dbPath = filepath.Join(home, dbPath[2:])
		}
	}

	// Ensure parent directory exists
	dbDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	conn, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Try to enable WAL mode for better concurrency
	// If it fails, continue with default journal mode (DELETE)
	if _, err := conn.Exec("PRAGMA journal_mode=WAL"); err != nil {
		// WAL mode failed, try DELETE mode
		conn.Exec("PRAGMA journal_mode=DELETE")
	}

	// Set synchronous mode to NORMAL for better performance
	conn.Exec("PRAGMA synchronous=NORMAL")

	// Create schema
	if _, err := conn.Exec(schemaSQL); err != nil {
		return nil, fmt.Errorf("failed to create schema: %w", err)
	}

	return &DB{conn: conn}, nil
}

// Close closes the database connection
func (db *DB) Close() error {
	return db.conn.Close()
}

// AddMediaFile adds a discovered media file to the database
func (db *DB) AddMediaFile(file *types.MediaFile) (int64, error) {
	audioJSON, _ := json.Marshal(file.AudioStreamsJSON)
	subtitleJSON, _ := json.Marshal(file.SubtitleStreamsJSON)

	result, err := db.conn.Exec(`
		INSERT OR IGNORE INTO media_files (
			file_path, file_name, file_size_bytes,
			codec, resolution_width, resolution_height,
			duration_seconds, bitrate_kbps, fps,
			audio_streams_json, subtitle_streams_json,
			should_transcode, transcoding_priority,
			estimated_size_reduction_percent
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		file.FilePath, file.FileName, file.FileSizeBytes,
		file.Codec, file.ResolutionWidth, file.ResolutionHeight,
		file.DurationSeconds, file.BitrateKbps, file.FPS,
		string(audioJSON), string(subtitleJSON),
		file.ShouldTranscode, file.TranscodingPriority,
		file.EstimatedSizeReductionPercent,
	)

	if err != nil {
		return 0, fmt.Errorf("failed to insert media file: %w", err)
	}

	return result.LastInsertId()
}

// UpdateMediaFile updates an existing media file
func (db *DB) UpdateMediaFile(id int64, file *types.MediaFile) error {
	audioJSON, _ := json.Marshal(file.AudioStreamsJSON)
	subtitleJSON, _ := json.Marshal(file.SubtitleStreamsJSON)

	_, err := db.conn.Exec(`
		UPDATE media_files SET
			file_name = ?, file_size_bytes = ?,
			codec = ?, resolution_width = ?, resolution_height = ?,
			duration_seconds = ?, bitrate_kbps = ?, fps = ?,
			audio_streams_json = ?, subtitle_streams_json = ?,
			should_transcode = ?, transcoding_priority = ?,
			estimated_size_reduction_percent = ?,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`,
		file.FileName, file.FileSizeBytes,
		file.Codec, file.ResolutionWidth, file.ResolutionHeight,
		file.DurationSeconds, file.BitrateKbps, file.FPS,
		string(audioJSON), string(subtitleJSON),
		file.ShouldTranscode, file.TranscodingPriority,
		file.EstimatedSizeReductionPercent,
		id,
	)

	return err
}

// GetMediaFileByPath retrieves a media file by its path
func (db *DB) GetMediaFileByPath(path string) (*types.MediaFile, error) {
	row := db.conn.QueryRow("SELECT * FROM media_files WHERE file_path = ?", path)
	file, err := db.scanMediaFile(row)
	if err == sql.ErrNoRows {
		return nil, nil // File not found - this is not an error
	}
	return file, err
}

// CreateJob creates a new transcode job from a media file
func (db *DB) CreateJob(mediaFileID int64, priority int) (int64, error) {
	result, err := db.conn.Exec(`
		INSERT INTO transcode_jobs (
			media_file_id, file_path, file_name, file_size_bytes, priority
		)
		SELECT id, file_path, file_name, file_size_bytes, ?
		FROM media_files WHERE id = ?
	`, priority, mediaFileID)

	if err != nil {
		return 0, fmt.Errorf("failed to create job: %w", err)
	}

	return result.LastInsertId()
}

// ClaimNextJob atomically claims the next job for a worker
// Priority order:
// 1. Orphaned jobs (downloading/transcoding/uploading with no worker) - recovered from restart
// 2. New queued jobs
func (db *DB) ClaimNextJob(workerID string) (*types.TranscodeJob, error) {
	tx, err := db.conn.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	var jobID int64

	// First, try to claim orphaned jobs (highest priority - these are recovered jobs)
	row := tx.QueryRow(`
		SELECT id FROM transcode_jobs
		WHERE status IN ('downloading', 'transcoding', 'uploading')
		  AND (worker_id = '' OR worker_id IS NULL)
		ORDER BY
		  CASE status
		    WHEN 'uploading' THEN 1
		    WHEN 'transcoding' THEN 2
		    WHEN 'downloading' THEN 3
		  END,
		  priority DESC,
		  created_at ASC
		LIMIT 1
	`)

	err = row.Scan(&jobID)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to find orphaned job: %w", err)
	}

	// If no orphaned jobs, try to find new queued jobs
	if err == sql.ErrNoRows {
		row = tx.QueryRow(`
			SELECT id FROM transcode_jobs
			WHERE status = 'queued' AND (worker_id = '' OR worker_id IS NULL)
			ORDER BY priority DESC, created_at ASC
			LIMIT 1
		`)

		if err := row.Scan(&jobID); err != nil {
			if err == sql.ErrNoRows {
				return nil, nil // No jobs available
			}
			return nil, fmt.Errorf("failed to find queued job: %w", err)
		}

		// For new queued jobs, set status to downloading and timestamp
		_, err = tx.Exec(`
			UPDATE transcode_jobs
			SET status = 'downloading',
			    worker_id = ?,
			    transcode_started_at = CURRENT_TIMESTAMP,
			    updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, workerID, jobID)
	} else {
		// For orphaned jobs, just assign worker (keep existing status and progress)
		_, err = tx.Exec(`
			UPDATE transcode_jobs
			SET worker_id = ?,
			    updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, workerID, jobID)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to claim job: %w", err)
	}

	// Get the full job details
	jobRow := tx.QueryRow("SELECT * FROM transcode_jobs WHERE id = ?", jobID)
	job, err := db.scanTranscodeJob(jobRow)
	if err != nil {
		return nil, fmt.Errorf("failed to get job details: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return job, nil
}

// UpdateJobStatus updates a job's status and progress
func (db *DB) UpdateJobStatus(jobID int64, status types.JobStatus, stage types.ProcessingStage, progress float64) error {
	_, err := db.conn.Exec(`
		UPDATE transcode_jobs
		SET status = ?, stage = ?, progress = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, string(status), string(stage), progress, jobID)

	return err
}

// UpdateJobProgress updates the progress, stage, and fps fields
func (db *DB) UpdateJobProgress(jobID int64, progress float64, fps float64, stage string) error {
	_, err := db.conn.Exec(`
		UPDATE transcode_jobs
		SET progress = ?, encoding_fps = ?, stage = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, progress, fps, stage, jobID)

	return err
}

// UpdateJobFileSize updates the current transcoded file size
func (db *DB) UpdateJobFileSize(jobID int64, fileSize int64) error {
	_, err := db.conn.Exec(`
		UPDATE transcode_jobs
		SET transcoded_file_size_bytes = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, fileSize, jobID)

	return err
}

// CompleteJob marks a job as completed
func (db *DB) CompleteJob(jobID int64, outputSize int64, encodingTime int, fps float64) error {
	// Start a transaction to update both tables atomically
	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Update the job status
	_, err = tx.Exec(`
		UPDATE transcode_jobs
		SET status = 'completed',
		    progress = 100.0,
		    transcode_completed_at = CURRENT_TIMESTAMP,
		    transcoded_file_size_bytes = ?,
		    encoding_time_seconds = ?,
		    encoding_fps = ?,
		    verification_passed = 1,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, outputSize, encodingTime, fps, jobID)
	if err != nil {
		return fmt.Errorf("failed to update job: %w", err)
	}

	// Mark the media file as no longer needing transcoding
	_, err = tx.Exec(`
		UPDATE media_files
		SET should_transcode = 0,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = (SELECT media_file_id FROM transcode_jobs WHERE id = ?)
	`, jobID)
	if err != nil {
		return fmt.Errorf("failed to update media file: %w", err)
	}

	return tx.Commit()
}

// FailJob marks a job as failed
func (db *DB) FailJob(jobID int64, errorMsg string) error {
	_, err := db.conn.Exec(`
		UPDATE transcode_jobs
		SET status = 'failed',
		    error_message = ?,
		    retry_count = retry_count + 1,
		    last_retry_at = CURRENT_TIMESTAMP,
		    worker_id = '',
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, errorMsg, jobID)

	return err
}

// PauseJob pauses a running job
func (db *DB) PauseJob(jobID int64) error {
	_, err := db.conn.Exec(`
		UPDATE transcode_jobs
		SET status = 'paused', worker_id = '', updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, jobID)

	return err
}

// ResumeJob resumes a paused job
func (db *DB) ResumeJob(jobID int64) error {
	_, err := db.conn.Exec(`
		UPDATE transcode_jobs
		SET status = 'queued', updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, jobID)

	return err
}

// CancelJob cancels a job
func (db *DB) CancelJob(jobID int64) error {
	_, err := db.conn.Exec(`
		UPDATE transcode_jobs
		SET status = 'canceled', worker_id = '', updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, jobID)

	return err
}

// KillJob immediately cancels a job and marks it as canceled
// Unlike CancelJob, this forces termination regardless of state
func (db *DB) KillJob(jobID int64) error {
	query := `
		UPDATE transcode_jobs
		SET status = 'canceled',
		    error_message = 'Job killed by user',
		    worker_id = '',
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
		    AND status NOT IN ('completed', 'failed', 'canceled')
	`

	result, err := db.conn.Exec(query, jobID)
	if err != nil {
		return err
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("job not found or already terminated")
	}

	return nil
}

// DeleteJob permanently deletes a job from the database
// Only allowed for jobs in terminal states (queued, failed, completed, canceled)
func (db *DB) DeleteJob(jobID int64) error {
	query := `
		DELETE FROM transcode_jobs
		WHERE id = ?
		    AND status IN ('queued', 'failed', 'completed', 'canceled')
	`

	result, err := db.conn.Exec(query, jobID)
	if err != nil {
		return err
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("job not found or cannot be deleted (may be in progress)")
	}

	return nil
}

// RecoverJobs recovers jobs from an unclean shutdown
// - Clears worker_id from orphaned jobs (in-progress jobs from previous run)
// - Jobs keep their current status and progress
// - They will be picked up by ClaimNextJob() which prioritizes orphaned jobs
// Returns the number of jobs recovered
func (db *DB) RecoverJobs() (int, error) {
	// Clear worker_id from orphaned jobs but keep status and progress
	// For downloading jobs: reset progress to 0 (need to re-download)
	result1, err := db.conn.Exec(`
		UPDATE transcode_jobs
		SET worker_id = '',
		    progress = 0,
		    error_message = 'Job recovered after restart - will resume download',
		    updated_at = CURRENT_TIMESTAMP
		WHERE status = 'downloading'
	`)
	if err != nil {
		return 0, fmt.Errorf("failed to recover downloading jobs: %w", err)
	}

	downloadingCount, _ := result1.RowsAffected()

	// For transcoding/uploading jobs: keep progress and status
	result2, err := db.conn.Exec(`
		UPDATE transcode_jobs
		SET worker_id = '',
		    error_message = 'Job recovered after restart - will resume from checkpoint',
		    updated_at = CURRENT_TIMESTAMP
		WHERE status IN ('transcoding', 'uploading')
	`)
	if err != nil {
		return 0, fmt.Errorf("failed to recover transcoding/uploading jobs: %w", err)
	}

	transcodingCount, _ := result2.RowsAffected()

	totalRecovered := int(downloadingCount + transcodingCount)

	// Note: We don't automatically resume paused jobs
	// The user can manually resume them if needed

	return totalRecovered, nil
}

// QueueJobsForTranscoding creates transcode jobs for all media files
// that should be transcoded but don't already have a job
func (db *DB) QueueJobsForTranscoding(limit int) (int, error) {
	// Find media files that need transcoding and don't have existing jobs
	// Exclude completed, canceled, and failed jobs to allow re-queueing of failed files
	query := `
		SELECT mf.id, mf.file_path, mf.file_name, mf.file_size_bytes, mf.transcoding_priority
		FROM media_files mf
		LEFT JOIN transcode_jobs tj ON mf.id = tj.media_file_id
		    AND tj.status NOT IN ('completed', 'canceled', 'failed')
		WHERE mf.should_transcode = 1
		    AND tj.id IS NULL
		ORDER BY mf.transcoding_priority DESC, mf.file_size_bytes DESC
		LIMIT ?
	`

	rows, err := db.conn.Query(query, limit)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var mediaFileID int64
		var filePath, fileName string
		var fileSizeBytes int64
		var priority int

		if err := rows.Scan(&mediaFileID, &filePath, &fileName, &fileSizeBytes, &priority); err != nil {
			return count, err
		}

		// Create job with priority from media file
		_, err := db.CreateJob(mediaFileID, priority)
		if err != nil {
			return count, err
		}
		count++
	}

	return count, nil
}

// UpdateJobPriority updates a job's priority
func (db *DB) UpdateJobPriority(jobID int64, priority int) error {
	_, err := db.conn.Exec(`
		UPDATE transcode_jobs
		SET priority = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, priority, jobID)

	return err
}

// GetJob retrieves a job by ID
func (db *DB) GetJob(jobID int64) (*types.TranscodeJob, error) {
	row := db.conn.QueryRow("SELECT * FROM transcode_jobs WHERE id = ?", jobID)
	return db.scanTranscodeJob(row)
}

// GetQueuedJobs retrieves all queued jobs ordered by priority
func (db *DB) GetQueuedJobs(limit int) ([]*types.TranscodeJob, error) {
	query := `
		SELECT * FROM transcode_jobs
		WHERE status = 'queued'
		ORDER BY priority DESC, created_at ASC
	`
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := db.conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []*types.TranscodeJob
	for rows.Next() {
		job, err := db.scanTranscodeJobRow(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}

	return jobs, rows.Err()
}

// GetActiveJobs retrieves all currently active jobs
func (db *DB) GetActiveJobs() ([]*types.TranscodeJob, error) {
	rows, err := db.conn.Query(`
		SELECT * FROM transcode_jobs
		WHERE status IN ('downloading', 'transcoding', 'uploading')
		ORDER BY transcode_started_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []*types.TranscodeJob
	for rows.Next() {
		job, err := db.scanTranscodeJobRow(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}

	return jobs, rows.Err()
}

// GetCompletedJobs returns completed and failed jobs, most recent first
func (db *DB) GetCompletedJobs(limit int) ([]*types.TranscodeJob, error) {
	rows, err := db.conn.Query(`
		SELECT * FROM transcode_jobs
		WHERE status IN ('completed', 'failed', 'canceled')
		ORDER BY transcode_completed_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []*types.TranscodeJob
	for rows.Next() {
		job, err := db.scanTranscodeJobRow(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}

	return jobs, rows.Err()
}

// GetStatistics computes aggregate statistics
func (db *DB) GetStatistics() (*types.Statistics, error) {
	stats := &types.Statistics{}

	// Count totals
	row := db.conn.QueryRow(`
		SELECT
			COUNT(*) as total_files,
			COALESCE(SUM(CASE WHEN should_transcode = 1 THEN 1 ELSE 0 END), 0) as to_transcode,
			COALESCE(SUM(file_size_bytes), 0) as total_size_bytes
		FROM media_files
	`)

	var totalSizeBytes int64
	if err := row.Scan(&stats.TotalFiles, &stats.ToTranscode, &totalSizeBytes); err != nil {
		return nil, err
	}

	stats.TotalSize = totalSizeBytes

	// Count job statuses
	row = db.conn.QueryRow(`
		SELECT
			COALESCE(SUM(CASE WHEN status = 'queued' THEN 1 ELSE 0 END), 0) as queued,
			COALESCE(SUM(CASE WHEN status IN ('downloading', 'transcoding', 'uploading') THEN 1 ELSE 0 END), 0) as active,
			COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0) as completed,
			COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0) as failed,
			COALESCE(SUM(CASE WHEN status = 'completed' THEN file_size_bytes ELSE 0 END), 0) as original_size,
			COALESCE(SUM(CASE WHEN status = 'completed' THEN transcoded_file_size_bytes ELSE 0 END), 0) as transcoded_size
		FROM transcode_jobs
	`)

	var originalSize, transcodedSize int64
	if err := row.Scan(
		&stats.Queued, &stats.InProgress, &stats.Completed, &stats.Failed,
		&originalSize, &transcodedSize,
	); err != nil {
		return nil, err
	}

	if transcodedSize > 0 {
		stats.TranscodedSize = transcodedSize
		stats.SpaceSaved = originalSize - transcodedSize
		if originalSize > 0 {
			stats.SpaceSavedPercent = (float64(originalSize-transcodedSize) / float64(originalSize)) * 100
		}
	}

	// Average encoding stats
	row = db.conn.QueryRow(`
		SELECT
			COALESCE(AVG(encoding_fps), 0) as avg_fps,
			COALESCE(AVG(encoding_time_seconds), 0) as avg_time_seconds
		FROM transcode_jobs
		WHERE status = 'completed' AND encoding_fps IS NOT NULL
	`)

	var avgTimeSeconds float64
	if err := row.Scan(&stats.AvgEncodingFPS, &avgTimeSeconds); err != nil {
		return nil, err
	}

	stats.AvgEncodingTimeMinutes = avgTimeSeconds / 60.0

	// Last scan time
	lastScan, err := db.GetState("last_scan_time")
	if err == nil && lastScan != "" {
		t, _ := time.Parse(time.RFC3339, lastScan)
		stats.LastScanTime = &t
	}

	return stats, nil
}

// LogEvent logs a processing event
func (db *DB) LogEvent(jobID int64, eventType types.EventType, message string, details interface{}) error {
	var detailsJSON string
	if details != nil {
		b, _ := json.Marshal(details)
		detailsJSON = string(b)
	}

	_, err := db.conn.Exec(`
		INSERT INTO processing_log (job_id, event_type, message, details_json)
		VALUES (?, ?, ?, ?)
	`, jobID, string(eventType), message, detailsJSON)

	return err
}

// SetState stores a key-value pair in system state
func (db *DB) SetState(key, value string) error {
	_, err := db.conn.Exec(`
		INSERT OR REPLACE INTO system_state (key, value, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
	`, key, value)

	return err
}

// GetState retrieves a value from system state
func (db *DB) GetState(key string) (string, error) {
	var value string
	err := db.conn.QueryRow("SELECT value FROM system_state WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// Helper functions for scanning rows
func (db *DB) scanMediaFile(row *sql.Row) (*types.MediaFile, error) {
	file := &types.MediaFile{}
	var audioJSON, subtitleJSON string

	err := row.Scan(
		&file.ID, &file.FilePath, &file.FileName, &file.FileSizeBytes,
		&file.Codec, &file.ResolutionWidth, &file.ResolutionHeight,
		&file.DurationSeconds, &file.BitrateKbps, &file.FPS,
		&audioJSON, &subtitleJSON,
		&file.ShouldTranscode, &file.TranscodingPriority,
		&file.EstimatedSizeReductionPercent,
		&file.DiscoveredAt, &file.UpdatedAt,
	)

	if err != nil {
		return nil, err
	}

	json.Unmarshal([]byte(audioJSON), &file.AudioStreamsJSON)
	json.Unmarshal([]byte(subtitleJSON), &file.SubtitleStreamsJSON)

	return file, nil
}

func (db *DB) scanTranscodeJob(row *sql.Row) (*types.TranscodeJob, error) {
	job := &types.TranscodeJob{}
	var status, stage string
	var transcodeStartedAt, transcodeCompletedAt, lastRetryAt sql.NullTime
	var transcodedFileSizeBytes, encodingTimeSeconds sql.NullInt64
	var encodingFPS sql.NullFloat64
	var verificationPassed sql.NullBool
	var errorMessage sql.NullString

	err := row.Scan(
		&job.ID, &job.MediaFileID, &job.FilePath, &job.FileName, &job.FileSizeBytes,
		&status, &stage, &job.Progress, &job.WorkerID,
		&transcodeStartedAt, &transcodeCompletedAt,
		&transcodedFileSizeBytes, &encodingTimeSeconds, &encodingFPS,
		&verificationPassed, &errorMessage, &job.RetryCount, &lastRetryAt,
		&job.Priority, &job.CreatedAt, &job.UpdatedAt,
	)

	if err != nil {
		return nil, err
	}

	job.Status = types.JobStatus(status)
	job.Stage = types.ProcessingStage(stage)

	// Handle nullable fields
	if transcodeStartedAt.Valid {
		job.TranscodeStartedAt = &transcodeStartedAt.Time
	}
	if transcodeCompletedAt.Valid {
		job.TranscodeCompletedAt = &transcodeCompletedAt.Time
	}
	if transcodedFileSizeBytes.Valid {
		job.TranscodedFileSizeBytes = transcodedFileSizeBytes.Int64
	}
	if encodingTimeSeconds.Valid {
		job.EncodingTimeSeconds = int(encodingTimeSeconds.Int64)
	}
	if encodingFPS.Valid {
		job.EncodingFPS = encodingFPS.Float64
	}
	if verificationPassed.Valid {
		job.VerificationPassed = verificationPassed.Bool
	}
	if errorMessage.Valid {
		job.ErrorMessage = errorMessage.String
	}
	if lastRetryAt.Valid {
		job.LastRetryAt = &lastRetryAt.Time
	}

	return job, nil
}

func (db *DB) scanTranscodeJobRow(rows *sql.Rows) (*types.TranscodeJob, error) {
	job := &types.TranscodeJob{}
	var status, stage string
	var transcodeStartedAt, transcodeCompletedAt, lastRetryAt sql.NullTime
	var transcodedFileSizeBytes, encodingTimeSeconds sql.NullInt64
	var encodingFPS sql.NullFloat64
	var verificationPassed sql.NullBool
	var errorMessage sql.NullString

	err := rows.Scan(
		&job.ID, &job.MediaFileID, &job.FilePath, &job.FileName, &job.FileSizeBytes,
		&status, &stage, &job.Progress, &job.WorkerID,
		&transcodeStartedAt, &transcodeCompletedAt,
		&transcodedFileSizeBytes, &encodingTimeSeconds, &encodingFPS,
		&verificationPassed, &errorMessage, &job.RetryCount, &lastRetryAt,
		&job.Priority, &job.CreatedAt, &job.UpdatedAt,
	)

	if err != nil {
		return nil, err
	}

	job.Status = types.JobStatus(status)
	job.Stage = types.ProcessingStage(stage)

	// Handle nullable fields
	if transcodeStartedAt.Valid {
		job.TranscodeStartedAt = &transcodeStartedAt.Time
	}
	if transcodeCompletedAt.Valid {
		job.TranscodeCompletedAt = &transcodeCompletedAt.Time
	}
	if transcodedFileSizeBytes.Valid {
		job.TranscodedFileSizeBytes = transcodedFileSizeBytes.Int64
	}
	if encodingTimeSeconds.Valid {
		job.EncodingTimeSeconds = int(encodingTimeSeconds.Int64)
	}
	if encodingFPS.Valid {
		job.EncodingFPS = encodingFPS.Float64
	}
	if verificationPassed.Valid {
		job.VerificationPassed = verificationPassed.Bool
	}
	if errorMessage.Valid {
		job.ErrorMessage = errorMessage.String
	}
	if lastRetryAt.Valid {
		job.LastRetryAt = &lastRetryAt.Time
	}

	return job, nil
}
