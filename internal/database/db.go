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
	return db.scanMediaFile(row)
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
func (db *DB) ClaimNextJob(workerID string) (*types.TranscodeJob, error) {
	tx, err := db.conn.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Find highest priority unclaimed job
	row := tx.QueryRow(`
		SELECT id FROM transcode_jobs
		WHERE status = 'queued' AND (worker_id = '' OR worker_id IS NULL)
		ORDER BY priority DESC, created_at ASC
		LIMIT 1
	`)

	var jobID int64
	if err := row.Scan(&jobID); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // No jobs available
		}
		return nil, fmt.Errorf("failed to find job: %w", err)
	}

	// Claim the job
	_, err = tx.Exec(`
		UPDATE transcode_jobs
		SET status = 'downloading',
		    worker_id = ?,
		    transcode_started_at = CURRENT_TIMESTAMP,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, workerID, jobID)

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

// UpdateJobProgress updates only the progress field
func (db *DB) UpdateJobProgress(jobID int64, progress float64, fps float64) error {
	_, err := db.conn.Exec(`
		UPDATE transcode_jobs
		SET progress = ?, encoding_fps = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, progress, fps, jobID)

	return err
}

// CompleteJob marks a job as completed
func (db *DB) CompleteJob(jobID int64, outputSize int64, encodingTime int, fps float64) error {
	_, err := db.conn.Exec(`
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

	return err
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

	err := row.Scan(
		&job.ID, &job.MediaFileID, &job.FilePath, &job.FileName, &job.FileSizeBytes,
		&status, &stage, &job.Progress, &job.WorkerID,
		&job.TranscodeStartedAt, &job.TranscodeCompletedAt,
		&job.TranscodedFileSizeBytes, &job.EncodingTimeSeconds, &job.EncodingFPS,
		&job.VerificationPassed, &job.ErrorMessage, &job.RetryCount, &job.LastRetryAt,
		&job.Priority, &job.CreatedAt, &job.UpdatedAt,
	)

	if err != nil {
		return nil, err
	}

	job.Status = types.JobStatus(status)
	job.Stage = types.ProcessingStage(stage)

	return job, nil
}

func (db *DB) scanTranscodeJobRow(rows *sql.Rows) (*types.TranscodeJob, error) {
	job := &types.TranscodeJob{}
	var status, stage string

	err := rows.Scan(
		&job.ID, &job.MediaFileID, &job.FilePath, &job.FileName, &job.FileSizeBytes,
		&status, &stage, &job.Progress, &job.WorkerID,
		&job.TranscodeStartedAt, &job.TranscodeCompletedAt,
		&job.TranscodedFileSizeBytes, &job.EncodingTimeSeconds, &job.EncodingFPS,
		&job.VerificationPassed, &job.ErrorMessage, &job.RetryCount, &job.LastRetryAt,
		&job.Priority, &job.CreatedAt, &job.UpdatedAt,
	)

	if err != nil {
		return nil, err
	}

	job.Status = types.JobStatus(status)
	job.Stage = types.ProcessingStage(stage)

	return job, nil
}
