package proxy

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"apt-cache-proxy/internal/cache"
	"apt-cache-proxy/internal/config"
	"apt-cache-proxy/internal/logger"
	"apt-cache-proxy/internal/mirrors"
	"apt-cache-proxy/internal/stats"
)

type Handler struct{}

func NewHandler() *Handler {
	return &Handler{}
}

// HandleAll is the main handler for all proxy requests
func (h *Handler) HandleAll(w http.ResponseWriter, r *http.Request) {
	// Handle CONNECT method (HTTPS tunneling)
	if r.Method == http.MethodConnect {
		h.handleConnect(w, r)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/")
	
	// Clean up proxy-style URLs
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		parts := strings.SplitN(path, "/", 4)
		if len(parts) >= 4 {
			path = parts[3]
		}
	}

	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 {
		h.handleUnknown(w, r, path)
		return
	}

	distro := parts[0]
	pkgPath := parts[1]

	// Check if this is a managed distro
	upstreamKey := mirrors.GetUpstreamKey(distro, pkgPath)
	allMirrors := mirrors.GetAll()

	if _, ok := allMirrors[upstreamKey]; ok {
		h.handlePackage(w, r, distro, pkgPath)
		return
	}

	if _, ok := allMirrors[distro]; ok {
		h.handlePackage(w, r, distro, pkgPath)
		return
	}

	// Unknown distro - passthrough if enabled
	h.handleUnknown(w, r, path)
}

func (h *Handler) handlePackage(w http.ResponseWriter, r *http.Request, distro, pkgPath string) {
	stats.IncrementRequests()
	
	log := logger.Get()
	log.Infof("Request: /%s/%s", distro, pkgPath)
	stats.AddLog(fmt.Sprintf("Request: /%s/%s", distro, pkgPath), "INFO")

	// Check cache
	cachePath := cache.GetCachePath(distro, pkgPath)
	if cache.IsCacheValid(cachePath) {
		stats.IncrementCacheHits()
		h.serveFromCache(w, r, cachePath)
		return
	}

	// Cache miss - download from upstream
	stats.IncrementCacheMisses()
	
	upstreamKey := mirrors.GetUpstreamKey(distro, pkgPath)
	allMirrors := mirrors.GetAll()
	
	mirrorURLs, ok := allMirrors[upstreamKey]
	if !ok {
		// Fallback to distro itself
		mirrorURLs, ok = allMirrors[distro]
		if !ok {
			log.Warnf("No upstream configured for: %s", upstreamKey)
			http.Error(w, "Unsupported distro", http.StatusNotFound)
			return
		}
	}

	// Build full URLs
	upstreamURLs := make([]string, len(mirrorURLs))
	for i, mirror := range mirrorURLs {
		upstreamURLs[i] = fmt.Sprintf("%s/%s", strings.TrimSuffix(mirror, "/"), pkgPath)
	}

	log.Infof("MISS: %s -> %s", pkgPath, upstreamKey)
	stats.AddLog(fmt.Sprintf("MISS: %s -> %s", pkgPath, upstreamKey), "INFO")

	// Copy request headers
	headers := make(map[string]string)
	for k, v := range r.Header {
		if strings.ToLower(k) != "host" && len(v) > 0 {
			headers[k] = v[0]
		}
	}

	// Download and cache (this happens in a goroutine internally for streaming)
	resp, err := cache.StreamAndCache(upstreamURLs, cachePath, headers)
	if err != nil {
		// Check if it's a DNS error
		if strings.Contains(err.Error(), "DNS resolution failed") {
			log.Errorf("DNS resolution failed for all mirrors. Check network connectivity.")
			http.Error(w, "Upstream mirrors unreachable (DNS failure)", http.StatusBadGateway)
		} else {
			log.Errorf("Download failed: %v", err)
			http.Error(w, "Failed to download from upstream", http.StatusBadGateway)
		}
		return
	}
	defer resp.Body.Close()

	// Copy response headers
	for k, v := range resp.Header {
		for _, val := range v {
			w.Header().Add(k, val)
		}
	}

	w.WriteHeader(resp.StatusCode)
	
	// Stream response to client (this also writes to cache via TeeReader)
	written, err := io.Copy(w, resp.Body)
	if err != nil {
		// Only log if it's not a broken pipe (client disconnected)
		if !strings.Contains(err.Error(), "broken pipe") && 
		   !strings.Contains(err.Error(), "connection reset") {
			log.Warnf("Error streaming response: %v", err)
		}
		return
	}
	
	stats.AddBytesServed(written)
}

func (h *Handler) serveFromCache(w http.ResponseWriter, r *http.Request, cachePath string) {
	log := logger.Get()
	log.Infof("Serving from cache: %s", cachePath)
	stats.AddLog(fmt.Sprintf("HIT: %s", cachePath), "SUCCESS")

	file, err := os.Open(cachePath)
	if err != nil {
		log.Errorf("Error reading cache: %v", err)
		http.Error(w, "Error reading cache", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		log.Errorf("Error stating cache file: %v", err)
		http.Error(w, "Error reading cache", http.StatusInternalServerError)
		return
	}

	// Update access time
	os.Chtimes(cachePath, time.Now(), info.ModTime())

	// Serve file
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
	
	stats.AddBytesServed(info.Size())
}

func (h *Handler) handleUnknown(w http.ResponseWriter, r *http.Request, path string) {
	cfg := config.Get()
	
	if !cfg.PassthroughMode {
		http.Error(w, "Unknown distro and passthrough disabled", http.StatusNotFound)
		return
	}

	// Direct proxy
	log := logger.Get()
	
	targetURL := r.URL.String()
	if !strings.HasPrefix(targetURL, "http") {
		http.Error(w, "Invalid proxy request", http.StatusBadRequest)
		return
	}

	log.Infof("Direct proxying: %s", targetURL)
	stats.AddLog(fmt.Sprintf("PROXY: %s", targetURL), "INFO")

	client := &http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return nil
		},
	}

	proxyReq, err := http.NewRequest(r.Method, targetURL, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Copy headers
	for k, v := range r.Header {
		if strings.ToLower(k) != "host" {
			for _, val := range v {
				proxyReq.Header.Add(k, val)
			}
		}
	}

	resp, err := client.Do(proxyReq)
	if err != nil {
		log.Errorf("Proxy error: %v", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy response headers
	for k, v := range resp.Header {
		kl := strings.ToLower(k)
		if kl != "content-encoding" && kl != "content-length" && kl != "transfer-encoding" && kl != "connection" {
			for _, val := range v {
				w.Header().Add(k, val)
			}
		}
	}

	w.WriteHeader(resp.StatusCode)
	written, _ := io.Copy(w, resp.Body)
	stats.AddBytesServed(written)
}

func (h *Handler) handleConnect(w http.ResponseWriter, r *http.Request) {
	log := logger.Get()
	
	target := r.Host
	if target == "" {
		target = r.URL.Host
	}
	
	if target == "" {
		http.Error(w, "Cannot determine CONNECT target", http.StatusBadRequest)
		return
	}

	log.Infof("CONNECT tunneling to %s", target)
	stats.AddLog(fmt.Sprintf("CONNECT: %s", target), "INFO")

	// Connect to upstream
	upstreamConn, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		log.Errorf("CONNECT failed: %v", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer upstreamConn.Close()

	// Hijack the connection
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		log.Errorf("Hijack failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer clientConn.Close()

	// Send 200 Connection Established
	clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	// Bidirectional copy
	done := make(chan struct{}, 2)

	go func() {
		io.Copy(upstreamConn, clientConn)
		done <- struct{}{}
	}()

	go func() {
		io.Copy(clientConn, upstreamConn)
		done <- struct{}{}
	}()

	<-done
}
