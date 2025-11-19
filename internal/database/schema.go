package database

const schemaSQL = `
-- Media files discovered from remote library
CREATE TABLE IF NOT EXISTS media_files (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    file_path TEXT UNIQUE NOT NULL,
    file_name TEXT NOT NULL,
    file_size_bytes INTEGER NOT NULL,

    -- Video metadata
    codec TEXT NOT NULL,
    resolution_width INTEGER,
    resolution_height INTEGER,
    duration_seconds REAL,
    bitrate_kbps INTEGER,
    fps REAL,

    -- Audio/subtitle info (JSON)
    audio_streams_json TEXT,
    subtitle_streams_json TEXT,

    -- Analysis results
    should_transcode BOOLEAN DEFAULT 0,
    transcoding_priority INTEGER DEFAULT 0,
    estimated_size_reduction_percent INTEGER,

    -- Checksum for integrity verification
    source_checksum TEXT,
    source_checksum_algo TEXT,
    source_checksum_at TIMESTAMP,

    -- Timestamps
    discovered_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Transcode jobs (active, queued, completed)
CREATE TABLE IF NOT EXISTS transcode_jobs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_file_id INTEGER NOT NULL,
    file_path TEXT NOT NULL,
    file_name TEXT NOT NULL,
    file_size_bytes INTEGER NOT NULL,

    -- Status tracking
    status TEXT DEFAULT 'queued', -- queued, downloading, transcoding, uploading, completed, failed, paused, canceled
    stage TEXT DEFAULT '',        -- download, transcode, validate, upload
    progress REAL DEFAULT 0.0,    -- 0-100
    worker_id TEXT DEFAULT '',

    -- Transcoding details
    transcode_started_at TIMESTAMP,
    transcode_completed_at TIMESTAMP,
    transcoded_file_size_bytes INTEGER,
    encoding_time_seconds INTEGER,
    encoding_fps REAL,

    -- Verification
    verification_passed BOOLEAN DEFAULT 0,

    -- Checksum verification
    local_input_checksum TEXT,
    local_output_checksum TEXT,
    uploaded_checksum TEXT,
    checksum_verified BOOLEAN DEFAULT 0,

    -- Error handling
    error_message TEXT,
    retry_count INTEGER DEFAULT 0,
    last_retry_at TIMESTAMP,

    -- Priority
    priority INTEGER DEFAULT 0,

    -- Timestamps
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (media_file_id) REFERENCES media_files(id)
);

-- Processing event log
CREATE TABLE IF NOT EXISTS processing_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id INTEGER,
    event_type TEXT NOT NULL,
    message TEXT,
    details_json TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (job_id) REFERENCES transcode_jobs(id)
);

-- System state (key-value store)
CREATE TABLE IF NOT EXISTS system_state (
    key TEXT PRIMARY KEY,
    value TEXT,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Indexes for performance
CREATE INDEX IF NOT EXISTS idx_media_files_should_transcode ON media_files(should_transcode);
CREATE INDEX IF NOT EXISTS idx_media_files_priority ON media_files(transcoding_priority DESC);
CREATE INDEX IF NOT EXISTS idx_media_files_codec ON media_files(codec);

CREATE INDEX IF NOT EXISTS idx_jobs_status ON transcode_jobs(status);
CREATE INDEX IF NOT EXISTS idx_jobs_worker ON transcode_jobs(worker_id);
CREATE INDEX IF NOT EXISTS idx_jobs_priority ON transcode_jobs(priority DESC, created_at ASC);

CREATE INDEX IF NOT EXISTS idx_log_job ON processing_log(job_id);
CREATE INDEX IF NOT EXISTS idx_log_type ON processing_log(event_type);
`

// Schema version for migration tracking
const SchemaVersion = 2

// Migration SQL for upgrading from version 1 to version 2
const migrationV2SQL = `
-- Add checksum columns to media_files
ALTER TABLE media_files ADD COLUMN source_checksum TEXT;
ALTER TABLE media_files ADD COLUMN source_checksum_algo TEXT;
ALTER TABLE media_files ADD COLUMN source_checksum_at TIMESTAMP;

-- Add checksum columns to transcode_jobs
ALTER TABLE transcode_jobs ADD COLUMN local_input_checksum TEXT;
ALTER TABLE transcode_jobs ADD COLUMN local_output_checksum TEXT;
ALTER TABLE transcode_jobs ADD COLUMN uploaded_checksum TEXT;
ALTER TABLE transcode_jobs ADD COLUMN checksum_verified BOOLEAN DEFAULT 0;
`
