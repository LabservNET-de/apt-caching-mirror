package stats

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"apt-cache-proxy/internal/config"
	"apt-cache-proxy/internal/database"
	"apt-cache-proxy/internal/logger"
)

// Stats holds runtime statistics with atomic counters for thread-safety
type Stats struct {
	RequestsTotal uint64
	CacheHits     uint64
	CacheMisses   uint64
	BytesServed   uint64
	StartTime     time.Time
}

type FileStats struct {
	TotalFiles   int64
	TotalSize    int64
	DistroStats  map[string]DistroStat
	mu           sync.RWMutex
}

type DistroStat struct {
	Files int64 `json:"files"`
	Size  int64 `json:"size"`
}

type LogEntry struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

var (
	stats      Stats
	fileStats  FileStats
	logBuffer  []LogEntry
	logMu      sync.Mutex
	maxLogSize = 100
)

func init() {
	stats.StartTime = time.Now()
	fileStats.DistroStats = make(map[string]DistroStat)
}

// IncrementRequests atomically increments total requests
func IncrementRequests() {
	atomic.AddUint64(&stats.RequestsTotal, 1)
}

// IncrementCacheHits atomically increments cache hits
func IncrementCacheHits() {
	atomic.AddUint64(&stats.CacheHits, 1)
}

// IncrementCacheMisses atomically increments cache misses
func IncrementCacheMisses() {
	atomic.AddUint64(&stats.CacheMisses, 1)
}

// AddBytesServed atomically adds to bytes served
func AddBytesServed(bytes int64) {
	atomic.AddUint64(&stats.BytesServed, uint64(bytes))
}

// GetStats returns current statistics
func GetStats() map[string]interface{} {
	uptime := time.Since(stats.StartTime)
	
	return map[string]interface{}{
		"requests_total": atomic.LoadUint64(&stats.RequestsTotal),
		"cache_hits":     atomic.LoadUint64(&stats.CacheHits),
		"cache_misses":   atomic.LoadUint64(&stats.CacheMisses),
		"bytes_served":   atomic.LoadUint64(&stats.BytesServed),
		"uptime":         uptime.String(),
	}
}

// GetFileStats returns file statistics
func GetFileStats() map[string]interface{} {
	fileStats.mu.RLock()
	defer fileStats.mu.RUnlock()
	
	return map[string]interface{}{
		"total_files":           fileStats.TotalFiles,
		"total_size":            fileStats.TotalSize,
		"total_cache_size_mb":   float64(fileStats.TotalSize) / (1024 * 1024),
		"distro_stats":          fileStats.DistroStats,
	}
}

// UpdateFileStats recalculates file statistics (expensive operation)
func UpdateFileStats() error {
	cfg := config.Get()
	log := logger.Get()
	
	log.Debug("Starting file stats update...")
	
	totalFiles := int64(0)
	totalSize := int64(0)
	distroStats := make(map[string]DistroStat)
	
	// Walk the storage directory
	entries, err := os.ReadDir(cfg.StoragePathResolved)
	if err != nil {
		return err
	}
	
	// Process each distro directory concurrently
	type result struct {
		distro string
		files  int64
		size   int64
	}
	
	results := make(chan result, len(entries))
	var wg sync.WaitGroup
	
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name()[0] == '.' {
			continue
		}
		
		wg.Add(1)
		go func(distroName string) {
			defer wg.Done()
			
			distroPath := filepath.Join(cfg.StoragePathResolved, distroName)
			var dFiles, dSize int64
			
			filepath.Walk(distroPath, func(path string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return nil
				}
				dFiles++
				dSize += info.Size()
				return nil
			})
			
			results <- result{distro: distroName, files: dFiles, size: dSize}
		}(entry.Name())
	}
	
	// Close results channel when all goroutines finish
	go func() {
		wg.Wait()
		close(results)
	}()
	
	// Collect results
	for r := range results {
		totalFiles += r.files
		totalSize += r.size
		distroStats[r.distro] = DistroStat{Files: r.files, Size: r.size}
	}
	
	// Update global stats
	fileStats.mu.Lock()
	fileStats.TotalFiles = totalFiles
	fileStats.TotalSize = totalSize
	fileStats.DistroStats = distroStats
	fileStats.mu.Unlock()
	
	log.Debugf("File stats updated: %d files, %.2f MB", totalFiles, float64(totalSize)/(1024*1024))
	return nil
}

// AddLog adds a log entry to the buffer
func AddLog(message, level string) {
	logMu.Lock()
	defer logMu.Unlock()
	
	entry := LogEntry{
		Time:    time.Now().Format("15:04:05"),
		Level:   level,
		Message: message,
	}
	
	logBuffer = append(logBuffer, entry)
	if len(logBuffer) > maxLogSize {
		logBuffer = logBuffer[1:]
	}
}

// GetLogs returns recent log entries
func GetLogs() []LogEntry {
	logMu.Lock()
	defer logMu.Unlock()
	
	logs := make([]LogEntry, len(logBuffer))
	copy(logs, logBuffer)
	return logs
}

// LoadFromDB loads statistics from database
func LoadFromDB() error {
	db := database.Get()
	log := logger.Get()
	
	rows, err := db.Query("SELECT key, value FROM stats")
	if err != nil {
		return err
	}
	defer rows.Close()
	
	for rows.Next() {
		var key string
		var value uint64
		if err := rows.Scan(&key, &value); err != nil {
			continue
		}
		
		switch key {
		case "requests_total":
			atomic.StoreUint64(&stats.RequestsTotal, value)
		case "cache_hits":
			atomic.StoreUint64(&stats.CacheHits, value)
		case "cache_misses":
			atomic.StoreUint64(&stats.CacheMisses, value)
		case "bytes_served":
			atomic.StoreUint64(&stats.BytesServed, value)
		}
	}
	
	log.Info("Stats loaded from database")
	return nil
}

// SaveToDB saves statistics to database
func SaveToDB() error {
	db := database.Get()
	
	statsMap := map[string]uint64{
		"requests_total": atomic.LoadUint64(&stats.RequestsTotal),
		"cache_hits":     atomic.LoadUint64(&stats.CacheHits),
		"cache_misses":   atomic.LoadUint64(&stats.CacheMisses),
		"bytes_served":   atomic.LoadUint64(&stats.BytesServed),
	}
	
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	
	stmt, err := tx.Prepare("UPDATE stats SET value = ? WHERE key = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()
	
	for key, value := range statsMap {
		if _, err := stmt.Exec(value, key); err != nil {
			return err
		}
	}
	
	return tx.Commit()
}
