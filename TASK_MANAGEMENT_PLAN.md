# Task Management & Restart Recovery Implementation Plan

## Overview
This plan addresses proper task management, restart recovery, and the complete workflow from scanning to job creation to transcoding.

## Current State Analysis

### What Exists
- ✅ Database schema with `media_files`, `transcode_jobs`, `processing_log`, `system_state` tables
- ✅ Job status tracking: `queued`, `downloading`, `transcoding`, `uploading`, `completed`, `failed`, `paused`, `canceled`
- ✅ Job stage tracking: `download`, `transcode`, `validate`, `upload`
- ✅ Database methods: `CreateJob()`, `ClaimNextJob()`, `UpdateJobStatus()`, etc.
- ✅ Scanner discovers files and stores in `media_files` table with `should_transcode` flag
- ✅ Scanner sets `transcoding_priority` (0-100) based on resolution and file size

### What's Missing
- ❌ **No job creation workflow** - Scan stores files but never creates jobs in `transcode_jobs` table
- ❌ **No UI command to queue jobs** - Tab 2 (Queue) only has scan button, no "Create Jobs" button
- ❌ **No scan checkpoint/resume** - Scan restarts from beginning if interrupted
- ❌ **No job recovery on startup** - Jobs stuck in intermediate states stay orphaned
- ❌ **No graceful shutdown** - No signal handling to mark jobs as paused before exit
- ❌ **No worker heartbeat** - No way to detect crashed workers or stale jobs
- ❌ **No temp file cleanup** - Orphaned files in `Workers.WorkDir` accumulate

## Implementation Plan

---

## Phase 1: Job Creation Workflow

### 1.1 Add Database Method to Queue Jobs from Media Files
**File:** `internal/database/db.go`

Add method to create jobs for all files where `should_transcode=true`:

```go
// QueueJobsForTranscoding creates transcode jobs for all media files
// that should be transcoded but don't already have a job
func (db *DB) QueueJobsForTranscoding(limit int) (int, error) {
    // Find media files that need transcoding and don't have existing jobs
    query := `
        SELECT mf.id, mf.file_path, mf.file_name, mf.file_size_bytes, mf.transcoding_priority
        FROM media_files mf
        LEFT JOIN transcode_jobs tj ON mf.id = tj.media_file_id
            AND tj.status NOT IN ('completed', 'canceled')
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
```

### 1.2 Add UI Command to Queue Jobs
**File:** `internal/tui/model.go`

In `handleKeyPress()` around line 268, add new key binding:

```go
case "a":
    // Add/Queue jobs for transcoding (only in Queue view)
    if m.viewMode == ViewQueue {
        count, err := m.db.QueueJobsForTranscoding(100) // Queue up to 100 jobs
        if err != nil {
            m.errorMsg = fmt.Sprintf("Failed to queue jobs: %v", err)
        } else {
            m.statusMsg = fmt.Sprintf("Queued %d jobs for transcoding", count)
            m.addLog("INFO", fmt.Sprintf("Queued %d jobs", count))
            m.refreshData()
        }
        return m, nil
    }
```

Update footer to show `a=add jobs` when in Queue view.

**File:** `internal/tui/views.go`

Update `renderQueue()` to show instructions:
- Show "Press 'a' to add/queue jobs for discovered files" when queue is empty
- Show statistics: "X files need transcoding, Y jobs queued"

---

## Phase 2: Graceful Shutdown

### 2.1 Add Signal Handler
**File:** `cmd/transcoder/main.go`

```go
import (
    "os"
    "os/signal"
    "syscall"
    "context"
)

func main() {
    // ... existing setup ...

    // Create context for graceful shutdown
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    // Setup signal handler
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

    go func() {
        <-sigChan
        log.Println("Received shutdown signal, cleaning up...")

        // Mark in-progress jobs as paused
        if err := db.PauseInProgressJobs(); err != nil {
            log.Printf("Error pausing jobs: %v", err)
        }

        // Close scanner connection
        if scanner != nil {
            scanner.Close()
        }

        // Signal TUI to exit
        cancel()
    }()

    // ... existing TUI code ...
}
```

### 2.2 Add Database Method to Pause In-Progress Jobs
**File:** `internal/database/db.go`

```go
// PauseInProgressJobs marks all jobs in intermediate states as paused
// Used during graceful shutdown
func (db *DB) PauseInProgressJobs() error {
    query := `
        UPDATE transcode_jobs
        SET status = 'paused',
            updated_at = CURRENT_TIMESTAMP
        WHERE status IN ('downloading', 'transcoding', 'uploading')
    `

    result, err := db.conn.Exec(query)
    if err != nil {
        return err
    }

    count, _ := result.RowsAffected()
    if count > 0 {
        log.Printf("Paused %d in-progress jobs", count)
    }

    return nil
}
```

---

## Phase 3: Scan Checkpoint & Resume

### 3.1 Store Scan Progress in system_state
**File:** `internal/scanner/scanner.go`

Modify `scanPath()` to checkpoint every N directories:

```go
// After successfully scanning a directory (line ~207)
func (s *Scanner) scanPath(ctx context.Context, path string, progress *ScanProgress, progressCb ProgressCallback) error {
    // ... existing code ...

    // Checkpoint progress every 10 directories
    if progress.FilesScanned%50 == 0 {
        if err := s.saveCheckpoint(path); err != nil {
            s.logDebug("Failed to save checkpoint: %v", err)
        }
    }

    // ... rest of existing code ...
}

func (s *Scanner) saveCheckpoint(currentPath string) error {
    return s.db.SetSystemState("scan_checkpoint_path", currentPath)
}

func (s *Scanner) clearCheckpoint() error {
    return s.db.DeleteSystemState("scan_checkpoint_path")
}
```

### 3.2 Add Resume Logic
**File:** `internal/scanner/scanner.go`

Modify `Scan()` to check for checkpoint:

```go
func (s *Scanner) Scan(ctx context.Context, progressCb ProgressCallback) error {
    // Check for previous checkpoint
    checkpoint, err := s.db.GetSystemState("scan_checkpoint_path")
    resuming := (err == nil && checkpoint != "")

    if resuming {
        s.logDebug("Resuming scan from checkpoint: %s", checkpoint)
        // TODO: Skip paths that come before checkpoint alphabetically
    } else {
        s.logDebug("Starting fresh scan")
    }

    // ... existing scan logic ...

    // Clear checkpoint on successful completion
    if err := s.clearCheckpoint(); err != nil {
        s.logDebug("Failed to clear checkpoint: %v", err)
    }

    return nil
}
```

### 3.3 Add system_state Methods
**File:** `internal/database/db.go`

```go
func (db *DB) GetSystemState(key string) (string, error) {
    var value string
    err := db.conn.QueryRow("SELECT value FROM system_state WHERE key = ?", key).Scan(&value)
    return value, err
}

func (db *DB) SetSystemState(key, value string) error {
    _, err := db.conn.Exec(`
        INSERT INTO system_state (key, value, updated_at)
        VALUES (?, ?, CURRENT_TIMESTAMP)
        ON CONFLICT(key) DO UPDATE SET
            value = excluded.value,
            updated_at = CURRENT_TIMESTAMP
    `, key, value)
    return err
}

func (db *DB) DeleteSystemState(key string) error {
    _, err := db.conn.Exec("DELETE FROM system_state WHERE key = ?", key)
    return err
}
```

---

## Phase 4: Job Recovery on Startup

### 4.1 Add Recovery Method
**File:** `internal/database/db.go`

```go
// RecoverOrphanedJobs handles jobs left in intermediate states
// Called on application startup
func (db *DB) RecoverOrphanedJobs() (int, error) {
    // Jobs in intermediate states should be requeued or marked failed
    query := `
        UPDATE transcode_jobs
        SET status = CASE
            WHEN retry_count < 3 THEN 'queued'
            ELSE 'failed'
        END,
        retry_count = retry_count + 1,
        last_retry_at = CURRENT_TIMESTAMP,
        error_message = CASE
            WHEN retry_count >= 3 THEN 'Job abandoned after restart (max retries exceeded)'
            ELSE 'Job requeued after restart'
        END,
        worker_id = '',
        updated_at = CURRENT_TIMESTAMP
        WHERE status IN ('downloading', 'transcoding', 'uploading')
    `

    result, err := db.conn.Exec(query)
    if err != nil {
        return 0, err
    }

    count, _ := result.RowsAffected()
    return int(count), nil
}

// RecoverPausedJobs resumes jobs that were paused during shutdown
func (db *DB) RecoverPausedJobs() (int, error) {
    query := `
        UPDATE transcode_jobs
        SET status = 'queued',
            worker_id = '',
            updated_at = CURRENT_TIMESTAMP
        WHERE status = 'paused'
    `

    result, err := db.conn.Exec(query)
    if err != nil {
        return 0, err
    }

    count, _ := result.RowsAffected()
    return int(count), nil
}
```

### 4.2 Call Recovery on Startup
**File:** `cmd/transcoder/main.go`

```go
func main() {
    // ... after DB initialization ...

    // Recover orphaned/paused jobs
    orphanedCount, err := db.RecoverOrphanedJobs()
    if err != nil {
        log.Fatalf("Failed to recover orphaned jobs: %v", err)
    }
    if orphanedCount > 0 {
        log.Printf("Recovered %d orphaned jobs", orphanedCount)
    }

    pausedCount, err := db.RecoverPausedJobs()
    if err != nil {
        log.Fatalf("Failed to recover paused jobs: %v", err)
    }
    if pausedCount > 0 {
        log.Printf("Recovered %d paused jobs", pausedCount)
    }

    // ... continue with TUI startup ...
}
```

---

## Phase 5: Worker Heartbeat & Timeout Detection

### 5.1 Add Heartbeat Tracking
**File:** `internal/database/schema.go`

Update `transcode_jobs` table (migration needed):

```sql
ALTER TABLE transcode_jobs ADD COLUMN last_heartbeat TIMESTAMP;
```

### 5.2 Update Job Heartbeat
**File:** `internal/transcode/worker.go`

In worker processing loop, periodically update heartbeat:

```go
// In processJob() or similar worker method
ticker := time.NewTicker(30 * time.Second)
defer ticker.Stop()

go func() {
    for range ticker.C {
        db.UpdateJobHeartbeat(jobID)
    }
}()
```

### 5.3 Add Timeout Detection
**File:** `internal/database/db.go`

```go
// UpdateJobHeartbeat updates the last heartbeat timestamp
func (db *DB) UpdateJobHeartbeat(jobID int64) error {
    _, err := db.conn.Exec(`
        UPDATE transcode_jobs
        SET last_heartbeat = CURRENT_TIMESTAMP
        WHERE id = ?
    `, jobID)
    return err
}

// DetectStalledJobs finds jobs that haven't had a heartbeat in N minutes
func (db *DB) DetectStalledJobs(timeoutMinutes int) ([]*types.TranscodeJob, error) {
    query := `
        SELECT * FROM transcode_jobs
        WHERE status IN ('downloading', 'transcoding', 'uploading')
            AND last_heartbeat < datetime('now', '-' || ? || ' minutes')
    `

    rows, err := db.conn.Query(query, timeoutMinutes)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    var jobs []*types.TranscodeJob
    for rows.Next() {
        job, err := db.scanTranscodeJobRow(rows)
        if err != nil {
            return jobs, err
        }
        jobs = append(jobs, job)
    }

    return jobs, nil
}
```

### 5.4 Periodic Stall Check
**File:** `internal/tui/model.go`

In `tickMsg` handler (line ~118):

```go
case tickMsg:
    // ... existing refresh logic ...

    // Check for stalled jobs every minute
    if time.Since(m.lastStallCheck) > time.Minute {
        stalledJobs, err := m.db.DetectStalledJobs(5) // 5 minute timeout
        if err == nil && len(stalledJobs) > 0 {
            m.addLog("WARN", fmt.Sprintf("Detected %d stalled jobs", len(stalledJobs)))
            // Requeue stalled jobs
            for _, job := range stalledJobs {
                if job.RetryCount < 3 {
                    m.db.FailJob(job.ID, "Job stalled - worker not responding")
                    m.db.CreateJob(job.MediaFileID, job.Priority) // Create new job
                }
            }
        }
        m.lastStallCheck = time.Now()
    }
```

---

## Phase 6: Temp File Cleanup

### 6.1 Track Temp Files in Database
**File:** `internal/database/db.go`

```go
// SetJobTempFile records the temp file path for cleanup
func (db *DB) SetJobTempFile(jobID int64, tempFilePath string) error {
    return db.SetSystemState(fmt.Sprintf("job_%d_temp_file", jobID), tempFilePath)
}

// GetJobTempFile retrieves the temp file path
func (db *DB) GetJobTempFile(jobID int64) (string, error) {
    return db.GetSystemState(fmt.Sprintf("job_%d_temp_file", jobID))
}

// ClearJobTempFile removes the temp file tracking
func (db *DB) ClearJobTempFile(jobID int64) error {
    return db.DeleteSystemState(fmt.Sprintf("job_%d_temp_file", jobID))
}
```

### 6.2 Cleanup on Startup
**File:** `cmd/transcoder/main.go`

```go
func cleanupOrphanedTempFiles(db *database.DB, workDir string) error {
    // Get all temp file entries from system_state
    rows, err := db.conn.Query("SELECT key, value FROM system_state WHERE key LIKE 'job_%_temp_file'")
    if err != nil {
        return err
    }
    defer rows.Close()

    for rows.Next() {
        var key, filePath string
        if err := rows.Scan(&key, &filePath); err != nil {
            continue
        }

        // Check if file exists and delete it
        if _, err := os.Stat(filePath); err == nil {
            log.Printf("Removing orphaned temp file: %s", filePath)
            os.Remove(filePath)
        }

        // Clear the tracking entry
        db.DeleteSystemState(key)
    }

    return nil
}

// In main():
if err := cleanupOrphanedTempFiles(db, cfg.Workers.WorkDir); err != nil {
    log.Printf("Failed to cleanup temp files: %v", err)
}
```

---

## Phase 7: Testing Plan

### 7.1 Scan Restart Testing
1. Start scan of large media library
2. Kill process mid-scan (Ctrl+C)
3. Restart - verify scan resumes from checkpoint
4. Complete scan - verify checkpoint is cleared

### 7.2 Job Recovery Testing
1. Start transcoding with 2 workers
2. Kill process mid-transcode
3. Restart - verify:
   - Jobs in `downloading`/`transcoding`/`uploading` are requeued
   - Retry count incremented
   - After 3 retries, jobs marked as failed
4. Graceful shutdown (Ctrl+C) - verify jobs marked as `paused`
5. Restart - verify paused jobs are resumed

### 7.3 Worker Stall Testing
1. Start transcode job
2. Manually suspend worker process (kill -STOP)
3. Wait 6 minutes
4. Verify TUI detects stalled job and requeues it

### 7.4 Temp File Cleanup Testing
1. Start transcode job
2. Kill process mid-download
3. Verify temp file exists in workDir
4. Restart application
5. Verify temp file is deleted

### 7.5 Job Creation Workflow Testing
1. Run scan to discover 100 files needing transcode
2. Verify `media_files` table has entries with `should_transcode=true`
3. Go to Queue view, press 'q'
4. Verify jobs created in `transcode_jobs` table
5. Verify jobs ordered by priority
6. Press 'q' again - verify no duplicate jobs created

---

## Implementation Order

1. **Phase 1** - Job creation (needed for basic workflow)
2. **Phase 4** - Job recovery on startup (prevents orphaned jobs)
3. **Phase 2** - Graceful shutdown (cleaner than crashes)
4. **Phase 6** - Temp file cleanup (prevents disk filling)
5. **Phase 3** - Scan checkpointing (nice-to-have optimization)
6. **Phase 5** - Worker heartbeat (advanced reliability)

---

## Files to Modify

1. `internal/database/db.go` - Add all new DB methods
2. `internal/database/schema.go` - Add heartbeat column migration
3. `internal/scanner/scanner.go` - Add checkpoint save/resume
4. `internal/tui/model.go` - Add 'q' key handler, stall detection
5. `internal/tui/views.go` - Update Queue view rendering
6. `cmd/transcoder/main.go` - Add signal handler, recovery calls
7. `internal/transcode/worker.go` - Add heartbeat updates

---

## Database Migrations Needed

```sql
-- Migration 001: Add heartbeat tracking
ALTER TABLE transcode_jobs ADD COLUMN last_heartbeat TIMESTAMP DEFAULT CURRENT_TIMESTAMP;

-- Migration 002: Add index for heartbeat queries
CREATE INDEX IF NOT EXISTS idx_jobs_heartbeat ON transcode_jobs(status, last_heartbeat);
```

---

## Success Criteria

- ✅ Scan can be interrupted and resumed from checkpoint
- ✅ Jobs automatically created for files that need transcoding
- ✅ Ctrl+C during transcode marks jobs as paused, not lost
- ✅ Restart recovers all orphaned/paused jobs
- ✅ Stalled jobs (crashed workers) are automatically detected and requeued
- ✅ Temp files from crashed jobs are cleaned up on restart
- ✅ No data loss or corruption across restarts
- ✅ Maximum 3 retry attempts before marking job as permanently failed
