package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"transcoder/internal/config"
	"transcoder/internal/database"
	"transcoder/internal/logging"
	"transcoder/internal/scanner"
	"transcoder/internal/transcode"
	"transcoder/internal/tui"
	"transcoder/internal/version"
)

func runTUI() error {
	// Setup signal handling for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Load configuration
	configPath := os.ExpandEnv("$HOME/transcoder/config.yaml")
	cfg, err := config.Load(configPath)
	if err != nil {
		// Try default config
		cfg = config.Default()
		fmt.Printf("Warning: Could not load config from %s, using defaults\n", configPath)
		fmt.Println("Press Enter to continue...")
		_, _ = fmt.Scanln()
	}

	// Initialize application logging
	if err := logging.Init(cfg.Logging.File, cfg.Logging.Level); err != nil {
		fmt.Printf("Warning: Failed to initialize logging: %v\n", err)
	}
	defer logging.Close()

	logging.Info("Configuration loaded from %s", configPath)

	// Open database
	dbPath := os.ExpandEnv(cfg.Database.Path)
	db, err := database.New(dbPath)
	if err != nil {
		logging.Error("Failed to open database: %v", err)
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer func() { _ = db.Close() }()
	logging.Info("Database opened: %s", dbPath)

	// Recover jobs from previous run (requeue orphaned jobs)
	recoveredCount, err := db.RecoverJobs()
	if err != nil {
		logging.Warn("Failed to recover jobs: %v", err)
		fmt.Printf("Warning: Failed to recover jobs: %v\n", err)
	} else if recoveredCount > 0 {
		logging.Info("Recovered %d orphaned jobs from previous run", recoveredCount)
		fmt.Printf("Recovered %d orphaned jobs from previous run\n", recoveredCount)
		fmt.Println("Press Enter to continue...")
		_, _ = fmt.Scanln()
	}

	// Create scanner
	scan, err := scanner.New(cfg, db)
	if err != nil {
		return fmt.Errorf("failed to create scanner: %w", err)
	}

	// Create worker pool
	workerPool := transcode.NewWorkerPool(cfg, db, scan)

	// Start worker pool
	workerPool.Start()
	logging.Info("Worker pool started with %d workers", cfg.Workers.MaxWorkers)
	defer func() {
		fmt.Println("\nShutting down gracefully...")
		logging.Info("Shutting down worker pool...")
		workerPool.Stop()
		logging.Info("Worker pool stopped")
		fmt.Println("Worker pool stopped")
	}()

	// Create TUI model
	model := tui.New(cfg, db, scan, workerPool)

	// Run TUI in a goroutine so we can handle signals
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())

	// Handle signals in a separate goroutine
	go func() {
		select {
		case sig := <-sigChan:
			fmt.Printf("\nReceived signal: %v\n", sig)
			cancel()
			p.Quit()
		case <-ctx.Done():
			return
		}
	}()

	// Run TUI
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	return nil
}

func validateConfig(configPath string) error {
	if configPath == "" {
		configPath = os.ExpandEnv("$HOME/transcoder/config.yaml")
	}

	// Check if config file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return fmt.Errorf("config file not found: %s", configPath)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}

	// Print config summary
	fmt.Printf("Config: %s\n\n", configPath)
	fmt.Printf("Mode:       %s\n", cfg.Mode)

	if cfg.IsLocalMode() {
		fmt.Printf("Media paths:\n")
		for _, p := range cfg.Local.MediaPaths {
			fmt.Printf("  - %s\n", p)
		}
	} else {
		fmt.Printf("Remote:     %s@%s:%d\n", cfg.Remote.User, cfg.Remote.Host, cfg.Remote.Port)
		fmt.Printf("SSH key:    %s\n", cfg.Remote.SSHKey)
		fmt.Printf("Media paths:\n")
		for _, p := range cfg.Remote.MediaPaths {
			fmt.Printf("  - %s\n", p)
		}
		fmt.Printf("SSH pool:   %d connections\n", cfg.Remote.SSHPoolSize)
	}

	fmt.Printf("\nEncoder:    %s (quality: %d, preset: %s)\n", cfg.Encoder.Codec, cfg.Encoder.Quality, cfg.Encoder.Preset)
	fmt.Printf("Workers:    %d\n", cfg.Workers.MaxWorkers)
	fmt.Printf("Work dir:   %s\n", cfg.Workers.WorkDir)
	fmt.Printf("Database:   %s\n", cfg.Database.Path)
	fmt.Printf("Log file:   %s\n", cfg.Logging.File)
	fmt.Printf("Log level:  %s\n", cfg.Logging.Level)
	fmt.Printf("Extensions: %v\n", cfg.Files.Extensions)

	fmt.Printf("\nConfig is valid.\n")
	return nil
}

func main() {
	// Handle flags before anything else
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "-v":
			fmt.Printf("transcoder %s\n", version.GetVersion())
			return
		case "--validate-config", "--check-config":
			var cfgPath string
			if len(os.Args) > 2 {
				cfgPath = os.Args[2]
			}
			if err := validateConfig(cfgPath); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		case "--help", "-h":
			fmt.Printf("transcoder %s\n\n", version.GetVersion())
			fmt.Println("Usage: transcoder [options]")
			fmt.Println()
			fmt.Println("Options:")
			fmt.Println("  --version, -v              Show version")
			fmt.Println("  --validate-config [path]   Validate config file and show summary")
			fmt.Println("  --help, -h                 Show this help")
			fmt.Println()
			fmt.Println("Default config: ~/transcoder/config.yaml")
			return
		}
	}

	if err := runTUI(); err != nil {
		log.Fatal(err)
	}
}
