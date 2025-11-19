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
		fmt.Scanln()
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
	defer db.Close()
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
		fmt.Scanln()
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
	p := tea.NewProgram(model, tea.WithAltScreen())

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

func main() {
	if err := runTUI(); err != nil {
		log.Fatal(err)
	}
}
