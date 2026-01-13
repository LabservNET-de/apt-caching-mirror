package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"apt-cache-proxy/internal/cache"
	"apt-cache-proxy/internal/config"
	"apt-cache-proxy/internal/database"
	"apt-cache-proxy/internal/logger"
	"apt-cache-proxy/internal/mirrors"
	"apt-cache-proxy/internal/proxy"
	"apt-cache-proxy/internal/server"
	"apt-cache-proxy/internal/stats"
	"apt-cache-proxy/internal/worker"
)

func main() {
	// Initialize logger
	logger.Init()
	log := logger.Get()

	// Load configuration
	if err := config.Load(); err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Initialize database
	if err := database.Init(); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer database.Close()

	// Load data from database
	if err := stats.LoadFromDB(); err != nil {
		log.Warnf("Failed to load stats from DB: %v", err)
	}
	if err := mirrors.LoadFromDB(); err != nil {
		log.Warnf("Failed to load mirrors from DB: %v", err)
	}
	if err := cache.LoadBlacklistFromDB(); err != nil {
		log.Warnf("Failed to load blacklist from DB: %v", err)
	}

	// Initialize stats with file scan
	go func() {
		log.Info("Starting initial file stats scan...")
		if err := stats.UpdateFileStats(); err != nil {
			log.Errorf("Initial file stats update failed: %v", err)
		}
	}()

	// Start background worker pool
	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()
	worker.Start(workerCtx)

	// Initialize proxy handler
	proxyHandler := proxy.NewHandler()

	// Create HTTP server
	srv := server.New(proxyHandler)
	
	cfg := config.Get()
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	log.Infof("Starting APT Cache Proxy on %s", addr)

	// Start server in a goroutine
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info("Shutting down server...")

	// Graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Errorf("Server forced to shutdown: %v", err)
	}

	// Cancel background workers
	workerCancel()

	// Save final stats
	if err := stats.SaveToDB(); err != nil {
		log.Errorf("Failed to save stats: %v", err)
	}

	log.Info("Server stopped")
}
