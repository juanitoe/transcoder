package scanner

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"transcoder/internal/config"
	"transcoder/internal/database"
	"transcoder/internal/types"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Scanner handles remote media library scanning
type Scanner struct {
	cfg        *config.Config
	db         *database.DB
	sshClient  *ssh.Client
	sftpClient *sftp.Client
}

// New creates a new Scanner instance
func New(cfg *config.Config, db *database.DB) (*Scanner, error) {
	return &Scanner{
		cfg: cfg,
		db:  db,
	}, nil
}

// Connect establishes SSH/SFTP connection to remote server
func (s *Scanner) Connect(ctx context.Context) error {
	// Try to use SSH agent first (for passphrase-protected keys)
	var authMethods []ssh.AuthMethod

	// Try SSH agent
	if agentConn, err := net.Dial("unix", os.Getenv("SSH_AUTH_SOCK")); err == nil {
		authMethods = append(authMethods, ssh.PublicKeysCallback(agent.NewClient(agentConn).Signers))
	}

	// Try loading key file if agent failed or as fallback
	if len(authMethods) == 0 {
		keyPath := os.ExpandEnv(s.cfg.Remote.SSHKey)
		if strings.HasPrefix(keyPath, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("failed to get home directory: %w", err)
			}
			keyPath = filepath.Join(home, keyPath[2:])
		}

		// Read private key
		keyBytes, err := os.ReadFile(keyPath)
		if err != nil {
			return fmt.Errorf("failed to read SSH key: %w", err)
		}

		// Parse private key (will fail if passphrase-protected)
		signer, err := ssh.ParsePrivateKey(keyBytes)
		if err != nil {
			return fmt.Errorf("failed to parse SSH key: %w (try using ssh-add to add your key to the agent)", err)
		}

		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}

	// Configure SSH client
	sshConfig := &ssh.ClientConfig{
		User: s.cfg.Remote.User,
		Auth: authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // TODO: Use known_hosts
		Timeout:         30 * time.Second,
	}

	// Connect to SSH server
	addr := fmt.Sprintf("%s:%d", s.cfg.Remote.Host, s.cfg.Remote.Port)
	sshClient, err := ssh.Dial("tcp", addr, sshConfig)
	if err != nil {
		return fmt.Errorf("failed to connect to SSH server: %w", err)
	}
	s.sshClient = sshClient

	// Create SFTP client
	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		s.sshClient.Close()
		return fmt.Errorf("failed to create SFTP client: %w", err)
	}
	s.sftpClient = sftpClient

	return nil
}

// Close closes the SSH/SFTP connection
func (s *Scanner) Close() error {
	if s.sftpClient != nil {
		s.sftpClient.Close()
	}
	if s.sshClient != nil {
		s.sshClient.Close()
	}
	return nil
}

// ScanProgress represents scan progress information
type ScanProgress struct {
	CurrentPath   string
	FilesScanned  int
	FilesAdded    int
	FilesUpdated  int
	BytesScanned  int64
	ErrorCount    int
	LastError     error
}

// ProgressCallback is called periodically during scanning
type ProgressCallback func(progress ScanProgress)

// Scan scans all configured media paths and updates the database
func (s *Scanner) Scan(ctx context.Context, progressCb ProgressCallback) error {
	if s.sftpClient == nil {
		return fmt.Errorf("not connected - call Connect() first")
	}

	var totalProgress ScanProgress

	for _, mediaPath := range s.cfg.Remote.MediaPaths {
		err := s.scanPath(ctx, mediaPath, &totalProgress, progressCb)
		if err != nil {
			return fmt.Errorf("failed to scan %s: %w", mediaPath, err)
		}
	}

	return nil
}

// scanPath recursively scans a single media path
func (s *Scanner) scanPath(ctx context.Context, path string, progress *ScanProgress, progressCb ProgressCallback) error {
	// Check for cancellation
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Update progress
	progress.CurrentPath = path
	if progressCb != nil {
		progressCb(*progress)
	}

	// List directory contents
	entries, err := s.sftpClient.ReadDir(path)
	if err != nil {
		progress.ErrorCount++
		progress.LastError = fmt.Errorf("failed to read directory %s: %w", path, err)
		if progressCb != nil {
			progressCb(*progress)
		}
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
			if err := s.scanPath(ctx, fullPath, progress, progressCb); err != nil {
				return err
			}
		} else if s.isVideoFile(entry.Name()) {
			// Process video file
			progress.FilesScanned++
			progress.BytesScanned += entry.Size()

			// Extract metadata
			metadata, err := s.extractMetadata(ctx, fullPath)
			if err != nil {
				progress.ErrorCount++
				progress.LastError = fmt.Errorf("failed to extract metadata from %s: %w", fullPath, err)
				if progressCb != nil {
					progressCb(*progress)
				}
				continue
			}

			// Check if file exists in database
			existing, err := s.db.GetMediaFileByPath(fullPath)
			if err != nil {
				progress.ErrorCount++
				progress.LastError = fmt.Errorf("database error for %s: %w", fullPath, err)
				if progressCb != nil {
					progressCb(*progress)
				}
				continue
			}

			if existing == nil {
				// New file - add to database
				_, err := s.db.AddMediaFile(metadata)
				if err != nil {
					progress.ErrorCount++
					progress.LastError = fmt.Errorf("failed to add %s to database: %w", fullPath, err)
					if progressCb != nil {
						progressCb(*progress)
					}
					continue
				}
				progress.FilesAdded++
			} else {
				// Existing file - update if changed
				if existing.FileSizeBytes != metadata.FileSizeBytes {
					metadata.ID = existing.ID
					if err := s.db.UpdateMediaFile(existing.ID, metadata); err != nil {
						progress.ErrorCount++
						progress.LastError = fmt.Errorf("failed to update %s in database: %w", fullPath, err)
						if progressCb != nil {
							progressCb(*progress)
						}
						continue
					}
					progress.FilesUpdated++
				}
			}

			// Update progress
			if progressCb != nil {
				progressCb(*progress)
			}
		}
	}

	return nil
}

// isVideoFile checks if a file has a video extension
func (s *Scanner) isVideoFile(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	videoExts := []string{
		".mp4", ".mkv", ".avi", ".mov", ".m4v",
		".wmv", ".flv", ".webm", ".mpg", ".mpeg",
		".m2ts", ".ts", ".vob", ".ogv", ".3gp",
	}
	for _, videoExt := range videoExts {
		if ext == videoExt {
			return true
		}
	}
	return false
}

// extractMetadata extracts video metadata from a remote file using ffprobe
func (s *Scanner) extractMetadata(ctx context.Context, remotePath string) (*types.MediaFile, error) {
	// Use FFprobe to extract full metadata
	return s.ExtractMetadataWithFFprobe(ctx, remotePath)
}

// DownloadFile downloads a remote file to a local path with progress tracking
func (s *Scanner) DownloadFile(ctx context.Context, remotePath, localPath string, progressCb func(bytesRead int64, totalBytes int64)) error {
	if s.sftpClient == nil {
		return fmt.Errorf("not connected - call Connect() first")
	}

	// Open remote file
	remoteFile, err := s.sftpClient.Open(remotePath)
	if err != nil {
		return fmt.Errorf("failed to open remote file: %w", err)
	}
	defer remoteFile.Close()

	// Get file size
	stat, err := remoteFile.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat remote file: %w", err)
	}
	totalBytes := stat.Size()

	// Create local file
	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return fmt.Errorf("failed to create local directory: %w", err)
	}

	localFile, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("failed to create local file: %w", err)
	}
	defer localFile.Close()

	// Copy with progress tracking
	buf := make([]byte, 32*1024) // 32KB buffer
	var bytesRead int64

	for {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, err := remoteFile.Read(buf)
		if n > 0 {
			if _, writeErr := localFile.Write(buf[:n]); writeErr != nil {
				return fmt.Errorf("failed to write to local file: %w", writeErr)
			}
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
			return fmt.Errorf("failed to read from remote file: %w", err)
		}
	}

	return nil
}

// UploadFile uploads a local file to a remote path with progress tracking
func (s *Scanner) UploadFile(ctx context.Context, localPath, remotePath string, progressCb func(bytesWritten int64, totalBytes int64)) error {
	if s.sftpClient == nil {
		return fmt.Errorf("not connected - call Connect() first")
	}

	// Open local file
	localFile, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open local file: %w", err)
	}
	defer localFile.Close()

	// Get file size
	stat, err := localFile.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat local file: %w", err)
	}
	totalBytes := stat.Size()

	// Create remote directory if needed
	remoteDir := filepath.Dir(remotePath)
	if err := s.sftpClient.MkdirAll(remoteDir); err != nil {
		return fmt.Errorf("failed to create remote directory: %w", err)
	}

	// Create remote file
	remoteFile, err := s.sftpClient.Create(remotePath)
	if err != nil {
		return fmt.Errorf("failed to create remote file: %w", err)
	}
	defer remoteFile.Close()

	// Copy with progress tracking
	buf := make([]byte, 32*1024) // 32KB buffer
	var bytesWritten int64

	for {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, err := localFile.Read(buf)
		if n > 0 {
			if _, writeErr := remoteFile.Write(buf[:n]); writeErr != nil {
				return fmt.Errorf("failed to write to remote file: %w", writeErr)
			}
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
			return fmt.Errorf("failed to read from local file: %w", err)
		}
	}

	return nil
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
