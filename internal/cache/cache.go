package cache

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"apt-cache-proxy/internal/config"
	"apt-cache-proxy/internal/database"
	"apt-cache-proxy/internal/logger"
)

var (
	blacklistPatterns []string
	blacklistMu       sync.RWMutex
)

// LoadBlacklistFromDB loads blacklist patterns from database
func LoadBlacklistFromDB() error {
	db := database.Get()
	rows, err := db.Query("SELECT pattern FROM package_blacklist")
	if err != nil {
		return err
	}
	defer rows.Close()

	blacklistMu.Lock()
	defer blacklistMu.Unlock()

	blacklistPatterns = []string{}
	for rows.Next() {
		var pattern string
		if err := rows.Scan(&pattern); err != nil {
			continue
		}
		blacklistPatterns = append(blacklistPatterns, pattern)
	}

	log := logger.Get()
	log.Infof("Loaded %d blacklist patterns", len(blacklistPatterns))
	return nil
}

// IsBlacklisted checks if a filename matches any blacklist pattern
func IsBlacklisted(filename string) bool {
	blacklistMu.RLock()
	defer blacklistMu.RUnlock()

	for _, pattern := range blacklistPatterns {
		if strings.Contains(pattern, "*") {
			// Convert glob to regex
			regexPattern := strings.ReplaceAll(pattern, ".", "\\.")
			regexPattern = strings.ReplaceAll(regexPattern, "*", ".*")
			if matched, _ := regexp.MatchString("(?i)"+regexPattern, filename); matched {
				return true
			}
		} else if strings.Contains(strings.ToLower(filename), strings.ToLower(pattern)) {
			return true
		}
	}
	return false
}

// GetCachePath generates a cache file path for a distro and package path
func GetCachePath(distro, pkgPath string) string {
	cfg := config.Get()
	hash := md5.Sum([]byte(pkgPath))
	hashStr := hex.EncodeToString(hash[:])
	
	filename := filepath.Base(pkgPath)
	if filename == "" || filename == "." || filename == "/" {
		filename = "index"
	}
	
	cacheDir := filepath.Join(cfg.StoragePathResolved, distro, hashStr[:2])
	os.MkdirAll(cacheDir, 0755)
	
	return filepath.Join(cacheDir, fmt.Sprintf("%s_%s", hashStr, filename))
}

// IsCacheValid checks if a cache file is still valid
func IsCacheValid(cachePath string) bool {
	info, err := os.Stat(cachePath)
	if err != nil {
		return false
	}

	// Check if metadata file exists and matches
	metaPath := cachePath + ".meta"
	if metaData, err := os.ReadFile(metaPath); err == nil {
		var expectedSize int64
		if _, err := fmt.Sscanf(string(metaData), "%d", &expectedSize); err == nil {
			// Validate file size matches metadata
			if info.Size() != expectedSize {
				// Cache corrupted, remove it
				log := logger.Get()
				log.Warnf("Cache size mismatch: %s (expected %d, got %d). Removing corrupted cache.", cachePath, expectedSize, info.Size())
				os.Remove(cachePath)
				os.Remove(metaPath)
				return false
			}
		}
	}

	cfg := config.Get()
	if !cfg.CacheRetentionEnabled {
		return true
	}

	// Check file age based on modification time
	age := time.Since(info.ModTime())
	maxAge := time.Duration(cfg.CacheDays) * 24 * time.Hour
	return age < maxAge
}

// StreamAndCache downloads from upstream and caches the file while streaming to client
func StreamAndCache(urls []string, cachePath string, headers map[string]string) (*http.Response, error) {
	log := logger.Get()
	
	// Clean up any leftover temp file from previous failed download
	tempPath := cachePath + ".tmp"
	if _, err := os.Stat(tempPath); err == nil {
		os.Remove(tempPath)
	}
	
	var lastErr error
	errorCount := 0
	
	// Try each URL until one succeeds
	for _, url := range urls {
		resp, err := downloadAndCache(url, cachePath, headers)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		errorCount++
		
		// Only log first and last errors to reduce noise
		if errorCount == 1 || errorCount == len(urls) {
			log.Warnf("Mirror failed (%d/%d): %v", errorCount, len(urls), err)
		}
	}
	
	return nil, fmt.Errorf("all %d mirrors failed: %v", len(urls), lastErr)
}

// streamingReader wraps the response body to write to cache file while reading
type streamingReader struct {
	resp          *http.Response
	file          *os.File
	tempPath      string
	finalPath     string
	teeReader     io.Reader
	closed        bool
	expectedSize  int64
	writtenBytes  int64
}

func (sr *streamingReader) Read(p []byte) (n int, err error) {
	n, err = sr.teeReader.Read(p)
	sr.writtenBytes += int64(n)
	return n, err
}

func (sr *streamingReader) Close() error {
	if sr.closed {
		return nil
	}
	sr.closed = true
	
	log := logger.Get()
	
	// Close response body
	sr.resp.Body.Close()
	
	// Close and rename file
	if sr.file != nil {
		sr.file.Close()
		
		// Check if file was fully written
		info, err := os.Stat(sr.tempPath)
		if err != nil {
			log.Warnf("Failed to stat temp file: %v", err)
			os.Remove(sr.tempPath)
			return nil
		}
		
		// Validate file size if Content-Length was provided
		if sr.expectedSize > 0 && info.Size() != sr.expectedSize {
			log.Warnf("File size mismatch: expected %d bytes, got %d bytes. Discarding cache.", sr.expectedSize, info.Size())
			os.Remove(sr.tempPath)
			return nil
		}
		
		// Ensure we actually wrote something
		if info.Size() == 0 {
			log.Warnf("Empty file downloaded, discarding cache")
			os.Remove(sr.tempPath)
			return nil
		}
		
		// Save metadata with expected size for validation during cache hit
		metaPath := sr.finalPath + ".meta"
		metaFile, err := os.Create(metaPath)
		if err == nil {
			fmt.Fprintf(metaFile, "%d\n", info.Size())
			metaFile.Close()
		}
		
		// Atomic rename - only cache if download was complete
		if err := os.Rename(sr.tempPath, sr.finalPath); err != nil {
			log.Warnf("Failed to cache file: %v", err)
			os.Remove(sr.tempPath)
			os.Remove(metaPath)
		} else {
			log.Infof("Cached: %s (%d bytes)", sr.finalPath, info.Size())
		}
	}
	
	return nil
}

func downloadAndCache(url, cachePath string, headers map[string]string) (*http.Response, error) {
	log := logger.Get()
	log.Infof("Downloading: %s", url)
	
	client := &http.Client{
		Timeout: 120 * time.Second, // Increased timeout for large files
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return nil // Follow redirects
		},
	}
	
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	
	// Copy headers
	for k, v := range headers {
		if strings.ToLower(k) != "host" {
			req.Header.Set(k, v)
		}
	}
	
	resp, err := client.Do(req)
	if err != nil {
		// Check for DNS errors
		if strings.Contains(err.Error(), "no such host") || 
		   strings.Contains(err.Error(), "Temporary failure in name resolution") {
			return nil, fmt.Errorf("DNS resolution failed")
		}
		return nil, err
	}
	
	if resp.StatusCode >= 400 {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	
	// Create temp file for atomic write
	tempPath := cachePath + ".tmp"
	file, err := os.Create(tempPath)
	if err != nil {
		resp.Body.Close()
		return nil, err
	}
	
	// Create streaming reader that writes to cache while being read
	sr := &streamingReader{
		resp:         resp,
		file:         file,
		tempPath:     tempPath,
		finalPath:    cachePath,
		teeReader:    io.TeeReader(resp.Body, file),
		expectedSize: resp.ContentLength,
		writtenBytes: 0,
	}
	
	// Create new response with streaming reader
	streamResp := &http.Response{
		StatusCode:    resp.StatusCode,
		Status:        resp.Status,
		Proto:         resp.Proto,
		ProtoMajor:    resp.ProtoMajor,
		ProtoMinor:    resp.ProtoMinor,
		Header:        resp.Header,
		Body:          sr,
		ContentLength: resp.ContentLength,
	}
	
	return streamResp, nil
}

func createResponseFromFile(cachePath string, statusCode int, originalHeaders http.Header) (*http.Response, error) {
	file, err := os.Open(cachePath)
	if err != nil {
		return nil, err
	}
	
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	
	resp := &http.Response{
		StatusCode: statusCode,
		Header:     make(http.Header),
		Body:       file,
		ContentLength: info.Size(),
	}
	
	// Copy relevant headers
	for k, v := range originalHeaders {
		kl := strings.ToLower(k)
		if kl != "content-encoding" && kl != "content-length" && kl != "transfer-encoding" {
			resp.Header[k] = v
		}
	}
	
	return resp, nil
}

// CleanOldCache removes old cache files based on retention policy
func CleanOldCache() error {
	cfg := config.Get()
	log := logger.Get()
	
	if !cfg.CacheRetentionEnabled {
		log.Info("Cache retention disabled, skipping cleanup")
		return nil
	}
	
	log.Info("Starting cache cleanup...")
	
	cutoffTime := time.Now().Add(-time.Duration(cfg.CacheDays) * 24 * time.Hour)
	cleanedCount := 0
	
	err := filepath.Walk(cfg.StoragePathResolved, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors
		}
		
		if info.IsDir() {
			return nil
		}
		
		// Check if file is older than cutoff
		if info.ModTime().Before(cutoffTime) {
			if err := os.Remove(path); err == nil {
				// Also remove metadata file if it exists
				metaPath := path + ".meta"
				os.Remove(metaPath)
				cleanedCount++
			}
		}
		
		return nil
	})
	
	log.Infof("Cache cleanup complete: removed %d files", cleanedCount)
	return err
}

// DeleteCachedFile deletes a specific cached file
func DeleteCachedFile(path string) error {
	cfg := config.Get()
	
	// Ensure path is within storage directory (security check)
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	
	if !strings.HasPrefix(absPath, cfg.StoragePathResolved) {
		return fmt.Errorf("invalid path: outside storage directory")
	}
	
	// Also remove metadata file
	metaPath := absPath + ".meta"
	os.Remove(metaPath)
	
	return os.Remove(absPath)
}

// AddBlacklistPattern adds a pattern to the blacklist
func AddBlacklistPattern(pattern string) error {
	db := database.Get()
	_, err := db.Exec("INSERT OR IGNORE INTO package_blacklist (pattern) VALUES (?)", pattern)
	if err != nil {
		return err
	}
	
	blacklistMu.Lock()
	blacklistPatterns = append(blacklistPatterns, pattern)
	blacklistMu.Unlock()
	
	log := logger.Get()
	log.Infof("Added blacklist pattern: %s", pattern)
	return nil
}

// RemoveBlacklistPattern removes a pattern from the blacklist
func RemoveBlacklistPattern(pattern string) error {
	db := database.Get()
	_, err := db.Exec("DELETE FROM package_blacklist WHERE pattern = ?", pattern)
	if err != nil {
		return err
	}
	
	blacklistMu.Lock()
	for i, p := range blacklistPatterns {
		if p == pattern {
			blacklistPatterns = append(blacklistPatterns[:i], blacklistPatterns[i+1:]...)
			break
		}
	}
	blacklistMu.Unlock()
	
	log := logger.Get()
	log.Infof("Removed blacklist pattern: %s", pattern)
	return nil
}

// GetBlacklistPatterns returns all blacklist patterns
func GetBlacklistPatterns() []string {
	blacklistMu.RLock()
	defer blacklistMu.RUnlock()
	
	patterns := make([]string, len(blacklistPatterns))
	copy(patterns, blacklistPatterns)
	return patterns
}
