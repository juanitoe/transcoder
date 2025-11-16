package main

import (
	"fmt"
	"log"
	"os"

	"transcoder/internal/config"
	"transcoder/internal/database"
)

func main() {
	fmt.Println("Video Transcoder TUI - Phase 1 Test")

	// Test config loading
	cfg := config.Default()
	fmt.Printf("✓ Config loaded (max workers: %d)\n", cfg.Workers.MaxWorkers)

	// Save example config if it doesn't exist
	configPath := os.ExpandEnv("$HOME/transcoder/config.yaml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := cfg.Save(configPath); err != nil {
			log.Fatalf("Failed to save config: %v", err)
		}
		fmt.Printf("✓ Example config saved to: %s\n", configPath)
	}

	// Test database connection
	dbPath := os.ExpandEnv("$HOME/transcoder/transcoder.db")
	db, err := database.New(dbPath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	fmt.Printf("✓ Database initialized: %s\n", dbPath)

	// Test statistics
	stats, err := db.GetStatistics()
	if err != nil {
		log.Fatalf("Failed to get statistics: %v", err)
	}

	fmt.Printf("✓ Statistics: %d total files, %d to transcode\n",
		stats.TotalFiles, stats.ToTranscode)

	fmt.Println("\n✅ Phase 1 foundations working correctly!")
	fmt.Println("\nNext steps:")
	fmt.Println("  - Phase 2: Remote scanner and metadata parser")
	fmt.Println("  - Phase 3: Transcoding engine and worker pool")
	fmt.Println("  - Phase 4: Bubbletea TUI")
}
