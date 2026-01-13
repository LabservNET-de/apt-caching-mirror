package mirrors

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"apt-cache-proxy/internal/database"
	"apt-cache-proxy/internal/logger"
)

type Mirror struct {
	URLs   []string `json:"urls"`
	Status string   `json:"status"`
}

var (
	mirrorsCache map[string]Mirror
	mu           sync.RWMutex
)

func init() {
	mirrorsCache = make(map[string]Mirror)
}

// LoadFromDB loads mirrors from database
func LoadFromDB() error {
	db := database.Get()
	log := logger.Get()
	
	rows, err := db.Query("SELECT name, urls, status FROM mirrors")
	if err != nil {
		return err
	}
	defer rows.Close()
	
	mu.Lock()
	defer mu.Unlock()
	
	mirrorsCache = make(map[string]Mirror)
	
	for rows.Next() {
		var name, urlsJSON, status string
		if err := rows.Scan(&name, &urlsJSON, &status); err != nil {
			continue
		}
		
		var urls []string
		if err := json.Unmarshal([]byte(urlsJSON), &urls); err != nil {
			continue
		}
		
		mirrorsCache[name] = Mirror{
			URLs:   urls,
			Status: status,
		}
	}
	
	log.Infof("Loaded %d mirrors from database", len(mirrorsCache))
	return nil
}

// GetAll returns all approved mirrors
func GetAll() map[string][]string {
	mu.RLock()
	defer mu.RUnlock()
	
	result := make(map[string][]string)
	for name, mirror := range mirrorsCache {
		if mirror.Status == "approved" {
			result[name] = mirror.URLs
		}
	}
	return result
}

// GetAllWithStatus returns all mirrors with their status (for admin)
func GetAllWithStatus() map[string]Mirror {
	mu.RLock()
	defer mu.RUnlock()
	
	result := make(map[string]Mirror)
	for name, mirror := range mirrorsCache {
		result[name] = mirror
	}
	return result
}

// GetUpstreamKey determines the upstream key from distro and path
func GetUpstreamKey(distro, pkgPath string) string {
	// Handle Ubuntu releases/pockets
	if strings.HasPrefix(distro, "ubuntu") || strings.Contains(distro, "noble") ||
		strings.Contains(distro, "jammy") || strings.Contains(distro, "focal") {
		return "ubuntu"
	}
	
	// Handle Debian releases
	if strings.HasPrefix(distro, "debian") || strings.Contains(distro, "bookworm") ||
		strings.Contains(distro, "bullseye") || strings.Contains(distro, "buster") {
		return "debian"
	}
	
	return distro
}

// Save saves a mirror to database
func Save(name string, urls []string, status string) error {
	// Validate: check for self-reference
	if isSelf(name) {
		log := logger.Get()
		log.Warnf("Skipping self-referencing mirror: %s", name)
		return nil
	}
	
	// Validate URLs
	validURLs := []string{}
	for _, url := range urls {
		if validateMirror(url) {
			validURLs = append(validURLs, url)
		}
	}
	
	if len(validURLs) == 0 {
		return nil
	}
	
	urlsJSON, err := json.Marshal(validURLs)
	if err != nil {
		return err
	}
	
	db := database.Get()
	_, err = db.Exec("INSERT OR REPLACE INTO mirrors (name, urls, status) VALUES (?, ?, ?)",
		name, string(urlsJSON), status)
	if err != nil {
		return err
	}
	
	// Update cache
	mu.Lock()
	mirrorsCache[name] = Mirror{
		URLs:   validURLs,
		Status: status,
	}
	mu.Unlock()
	
	log := logger.Get()
	log.Infof("Saved mirror: %s (%d URLs, status: %s)", name, len(validURLs), status)
	return nil
}

// Update updates a mirror's URLs or status
func Update(name string, urls []string, status string) error {
	db := database.Get()
	
	if urls != nil {
		urlsJSON, err := json.Marshal(urls)
		if err != nil {
			return err
		}
		_, err = db.Exec("UPDATE mirrors SET urls = ? WHERE name = ?", string(urlsJSON), name)
		if err != nil {
			return err
		}
	}
	
	if status != "" {
		_, err := db.Exec("UPDATE mirrors SET status = ? WHERE name = ?", status, name)
		if err != nil {
			return err
		}
	}
	
	// Reload from DB
	return LoadFromDB()
}

// Delete deletes a mirror
func Delete(name string) error {
	db := database.Get()
	_, err := db.Exec("DELETE FROM mirrors WHERE name = ?", name)
	if err != nil {
		return err
	}
	
	mu.Lock()
	delete(mirrorsCache, name)
	mu.Unlock()
	
	log := logger.Get()
	log.Infof("Deleted mirror: %s", name)
	return nil
}

func isSelf(host string) bool {
	hostname := strings.Split(host, ":")[0]
	
	if hostname == "localhost" || hostname == "127.0.0.1" || hostname == "::1" || hostname == "0.0.0.0" {
		return true
	}
	
	// Check if it resolves to a local IP
	ips, err := net.LookupIP(hostname)
	if err != nil {
		return false
	}
	
	// Get local IPs
	localIPs := getLocalIPs()
	
	for _, ip := range ips {
		for _, localIP := range localIPs {
			if ip.Equal(localIP) {
				return true
			}
		}
	}
	
	return false
}

func getLocalIPs() []net.IP {
	var ips []net.IP
	
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ips
	}
	
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok {
			ips = append(ips, ipnet.IP)
		}
	}
	
	return ips
}

func validateMirror(url string) bool {
	client := &http.Client{
		Timeout: 5 * time.Second,
	}
	
	resp, err := client.Head(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	
	return resp.StatusCode < 400
}
