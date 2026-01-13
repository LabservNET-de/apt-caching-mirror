package server

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"apt-cache-proxy/internal/cache"
	"apt-cache-proxy/internal/config"
	"apt-cache-proxy/internal/logger"
	"apt-cache-proxy/internal/mirrors"
	"apt-cache-proxy/internal/proxy"
	"apt-cache-proxy/internal/stats"

	"github.com/gorilla/mux"
)

//go:embed templates/dashboard.html
var dashboardHTML string

//go:embed templates/admin.html
var adminHTML string

// New creates a new HTTP server with all routes
func New(proxyHandler *proxy.Handler) *http.Server {
	r := mux.NewRouter()

	// Public routes (no auth)
	r.HandleFunc("/health", healthHandler).Methods("GET")
	r.HandleFunc("/api/stats", statsHandler).Methods("GET")
	r.HandleFunc("/api/cache/search", cacheSearchHandler).Methods("GET")
	r.HandleFunc("/acng-report.html", dashboardHandler).Methods("GET")
	r.HandleFunc("/", dashboardHandler).Methods("GET")
	r.HandleFunc("/admin", adminHandler).Methods("GET")
	
	// Admin API routes (authenticated)
	api := r.PathPrefix("/api").Subrouter()
	api.Use(authMiddleware)
	
	api.HandleFunc("/admin/config", getConfigHandler).Methods("GET")
	api.HandleFunc("/admin/config", updateConfigHandler).Methods("PUT")
	api.HandleFunc("/admin/mirrors", getMirrorsHandler).Methods("GET")
	api.HandleFunc("/admin/mirrors", addMirrorHandler).Methods("POST")
	api.HandleFunc("/admin/mirrors/{name}", updateMirrorHandler).Methods("PUT")
	api.HandleFunc("/admin/mirrors/{name}", deleteMirrorHandler).Methods("DELETE")
	api.HandleFunc("/admin/cache", deleteCacheFileHandler).Methods("DELETE")
	api.HandleFunc("/admin/blacklist", getBlacklistHandler).Methods("GET")
	api.HandleFunc("/admin/blacklist", addBlacklistHandler).Methods("POST")
	api.HandleFunc("/admin/blacklist", removeBlacklistHandler).Methods("DELETE")
	api.HandleFunc("/admin/cleanup", cleanupHandler).Methods("POST")
	api.HandleFunc("/reload", reloadHandler).Methods("POST")

	// Catch-all proxy handler (must be last)
	r.PathPrefix("/").HandlerFunc(proxyHandler.HandleAll)

	cfg := config.Get()
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)

	return &http.Server{
		Addr:    addr,
		Handler: r,
	}
}

func authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := config.Get()
		token := cfg.AdminToken
		
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		receivedToken := authHeader
		if strings.HasPrefix(authHeader, "Bearer ") {
			receivedToken = strings.TrimPrefix(authHeader, "Bearer ")
		}

		if receivedToken != token {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	json.NewEncoder(w).Encode(map[string]string{
		"status":     "ok",
		"cache_path": cfg.StoragePathResolved,
	})
}

func statsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	
	response := stats.GetStats()
	fileStats := stats.GetFileStats()
	
	for k, v := range fileStats {
		response[k] = v
	}
	
	response["mirrors"] = mirrors.GetAll()
	response["logs"] = stats.GetLogs()
	
	json.NewEncoder(w).Encode(response)
}

func dashboardHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(dashboardHTML))
}

func adminHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(adminHTML))
}

func getConfigHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"cache_days":              cfg.CacheDays,
		"cache_retention_enabled": cfg.CacheRetentionEnabled,
	})
}

func updateConfigHandler(w http.ResponseWriter, r *http.Request) {
	var data map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if days, ok := data["cache_days"].(float64); ok {
		config.Set("cache_days", int(days))
	}

	if enabled, ok := data["cache_retention_enabled"].(bool); ok {
		config.Set("cache_retention_enabled", enabled)
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

func getMirrorsHandler(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(mirrors.GetAllWithStatus())
}

func addMirrorHandler(w http.ResponseWriter, r *http.Request) {
	var data struct {
		Name   string   `json:"name"`
		URLs   []string `json:"urls"`
		Status string   `json:"status"`
	}

	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if data.Name == "" || len(data.URLs) == 0 {
		http.Error(w, "Missing name or urls", http.StatusBadRequest)
		return
	}

	if data.Status == "" {
		data.Status = "approved"
	}

	if err := mirrors.Save(data.Name, data.URLs, data.Status); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

func updateMirrorHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["name"]

	var data struct {
		URLs   []string `json:"urls"`
		Status string   `json:"status"`
	}

	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := mirrors.Update(name, data.URLs, data.Status); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

func deleteMirrorHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["name"]

	if err := mirrors.Delete(name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

func deleteCacheFileHandler(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "Missing path parameter", http.StatusBadRequest)
		return
	}

	if err := cache.DeleteCachedFile(path); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

func getBlacklistHandler(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(cache.GetBlacklistPatterns())
}

func addBlacklistHandler(w http.ResponseWriter, r *http.Request) {
	var data struct {
		Pattern string `json:"pattern"`
	}

	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if data.Pattern == "" {
		http.Error(w, "Missing pattern", http.StatusBadRequest)
		return
	}

	if err := cache.AddBlacklistPattern(data.Pattern); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

func removeBlacklistHandler(w http.ResponseWriter, r *http.Request) {
	pattern := r.URL.Query().Get("pattern")
	if pattern == "" {
		http.Error(w, "Missing pattern parameter", http.StatusBadRequest)
		return
	}

	if err := cache.RemoveBlacklistPattern(pattern); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

func cleanupHandler(w http.ResponseWriter, r *http.Request) {
	go cache.CleanOldCache()
	json.NewEncoder(w).Encode(map[string]string{"status": "cleanup started"})
}

func reloadHandler(w http.ResponseWriter, r *http.Request) {
	if err := config.Reload(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	mirrors.LoadFromDB()
	cache.LoadBlacklistFromDB()

	log := logger.Get()
	log.Info("Configuration reloaded")

	json.NewEncoder(w).Encode(map[string]string{"status": "reloaded"})
}

func cacheSearchHandler(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		json.NewEncoder(w).Encode([]interface{}{})
		return
	}

	cfg := config.Get()
	var results []map[string]interface{}

	// Simple file search implementation
	// Walk through storage directory and find matching files
	err := filepath.Walk(cfg.StoragePathResolved, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}

		filename := filepath.Base(path)
		if strings.Contains(strings.ToLower(filename), strings.ToLower(query)) {
			// Extract distro from path
			relPath, _ := filepath.Rel(cfg.StoragePathResolved, path)
			distro := strings.Split(relPath, string(filepath.Separator))[0]

			results = append(results, map[string]interface{}{
				"name":   filename,
				"distro": distro,
				"size":   info.Size(),
				"path":   path,
				"mtime":  info.ModTime().Format("2006-01-02 15:04"),
				"atime":  info.ModTime().Format("2006-01-02 15:04"),
			})
		}

		// Limit results to 100
		if len(results) >= 100 {
			return filepath.SkipDir
		}

		return nil
	})

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}
