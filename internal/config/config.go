package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type Config struct {
	Host                   string `json:"host"`
	Port                   int    `json:"port"`
	StoragePath            string `json:"storage_path"`
	DatabasePath           string `json:"database_path"`
	CacheDays              int    `json:"cache_days"`
	CacheRetentionEnabled  bool   `json:"cache_retention_enabled"`
	LogLevel               string `json:"log_level"`
	PassthroughMode        bool   `json:"passthrough_mode"`
	AdminToken             string `json:"admin_token"`
	
	// Resolved paths (computed at runtime)
	StoragePathResolved  string `json:"-"`
	DatabasePathResolved string `json:"-"`
	BaseDir              string `json:"-"`
}

var (
	cfg  *Config
	mu   sync.RWMutex
	once sync.Once
)

// Load loads configuration from config.json
func Load() error {
	var err error
	once.Do(func() {
		err = reload()
	})
	return err
}

// Reload reloads configuration from disk
func Reload() error {
	mu.Lock()
	defer mu.Unlock()
	return reload()
}

func reload() error {
	// Get base directory
	baseDir, err := os.Getwd()
	if err != nil {
		return err
	}

	configPath := filepath.Join(baseDir, "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}

	var newCfg Config
	if err := json.Unmarshal(data, &newCfg); err != nil {
		return err
	}

	// Set defaults
	if newCfg.Host == "" {
		newCfg.Host = "0.0.0.0"
	}
	if newCfg.Port == 0 {
		newCfg.Port = 8080
	}
	if newCfg.StoragePath == "" {
		newCfg.StoragePath = "storage"
	}
	if newCfg.DatabasePath == "" {
		newCfg.DatabasePath = "data/stats.db"
	}
	if newCfg.CacheDays == 0 {
		newCfg.CacheDays = 7
	}
	if newCfg.LogLevel == "" {
		newCfg.LogLevel = "INFO"
	}

	// Resolve paths
	newCfg.BaseDir = baseDir
	if filepath.IsAbs(newCfg.StoragePath) {
		newCfg.StoragePathResolved = newCfg.StoragePath
	} else {
		newCfg.StoragePathResolved = filepath.Join(baseDir, newCfg.StoragePath)
	}

	if filepath.IsAbs(newCfg.DatabasePath) {
		newCfg.DatabasePathResolved = newCfg.DatabasePath
	} else {
		newCfg.DatabasePathResolved = filepath.Join(baseDir, newCfg.DatabasePath)
	}

	// Create storage directory
	if err := os.MkdirAll(newCfg.StoragePathResolved, 0755); err != nil {
		return err
	}

	// Create database directory
	dbDir := filepath.Dir(newCfg.DatabasePathResolved)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return err
	}

	cfg = &newCfg
	return nil
}

// Get returns the current configuration
func Get() *Config {
	mu.RLock()
	defer mu.RUnlock()
	return cfg
}

// Set updates a configuration value
func Set(key string, value interface{}) error {
	mu.Lock()
	defer mu.Unlock()

	switch key {
	case "cache_days":
		if v, ok := value.(int); ok {
			cfg.CacheDays = v
		}
	case "cache_retention_enabled":
		if v, ok := value.(bool); ok {
			cfg.CacheRetentionEnabled = v
		}
	}

	// Save to disk
	return saveConfig()
}

func saveConfig() error {
	configPath := filepath.Join(cfg.BaseDir, "config.json")
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0644)
}
