package worker

import (
	"context"
	"time"

	"apt-cache-proxy/internal/cache"
	"apt-cache-proxy/internal/logger"
	"apt-cache-proxy/internal/stats"
)

// Start starts the background worker pool
func Start(ctx context.Context) {
	log := logger.Get()
	log.Info("Starting background workers")

	// Worker 1: Save stats every minute
	go statsSaver(ctx)

	// Worker 2: Update file stats every 5 minutes
	go fileStatsUpdater(ctx)

	// Worker 3: Clean cache every hour
	go cacheCleaner(ctx)
}

func statsSaver(ctx context.Context) {
	log := logger.Get()
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("Stats saver worker stopped")
			return
		case <-ticker.C:
			if err := stats.SaveToDB(); err != nil {
				log.Errorf("Failed to save stats: %v", err)
			}
		}
	}
}

func fileStatsUpdater(ctx context.Context) {
	log := logger.Get()
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("File stats updater worker stopped")
			return
		case <-ticker.C:
			log.Debug("Updating file stats...")
			if err := stats.UpdateFileStats(); err != nil {
				log.Errorf("Failed to update file stats: %v", err)
			}
		}
	}
}

func cacheCleaner(ctx context.Context) {
	log := logger.Get()
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("Cache cleaner worker stopped")
			return
		case <-ticker.C:
			log.Info("Running cache cleanup...")
			if err := cache.CleanOldCache(); err != nil {
				log.Errorf("Cache cleanup failed: %v", err)
			}
		}
	}
}
