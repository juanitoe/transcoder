package main

import (
	"fmt"
	"log"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"transcoder/internal/config"
	"transcoder/internal/database"
	"transcoder/internal/scanner"
	"transcoder/internal/transcode"
	"transcoder/internal/tui"
)

func runTUI() error {
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

	// Open database
	dbPath := os.ExpandEnv(cfg.Database.Path)
	db, err := database.New(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()

	// Recover jobs from previous run (requeue orphaned jobs)
	recoveredCount, err := db.RecoverJobs()
	if err != nil {
		fmt.Printf("Warning: Failed to recover jobs: %v\n", err)
	} else if recoveredCount > 0 {
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
	defer workerPool.Stop()

	// Create TUI model
	model := tui.New(cfg, db, scan, workerPool)

	// Run TUI
	p := tea.NewProgram(model, tea.WithAltScreen())
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
