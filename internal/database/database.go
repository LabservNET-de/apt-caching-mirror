package database

import (
	"database/sql"
	"sync"

	"apt-cache-proxy/internal/config"
	"apt-cache-proxy/internal/logger"

	_ "github.com/mattn/go-sqlite3"
)

var (
	db   *sql.DB
	mu   sync.Mutex
	once sync.Once
)

// Init initializes the database connection
func Init() error {
	var err error
	once.Do(func() {
		cfg := config.Get()
		log := logger.Get()

		db, err = sql.Open("sqlite3", cfg.DatabasePathResolved+"?_journal_mode=WAL&_busy_timeout=5000")
		if err != nil {
			return
		}

		// Set connection pool settings for better concurrency
		db.SetMaxOpenConns(25)
		db.SetMaxIdleConns(10)

		// Create tables
		err = createTables()
		if err != nil {
			return
		}

		log.Info("Database initialized successfully")
	})
	return err
}

// Get returns the database connection
func Get() *sql.DB {
	return db
}

// Close closes the database connection
func Close() error {
	if db != nil {
		return db.Close()
	}
	return nil
}

func createTables() error {
	schema := `
	CREATE TABLE IF NOT EXISTS stats (
		key TEXT PRIMARY KEY,
		value INTEGER
	);

	CREATE TABLE IF NOT EXISTS mirrors (
		name TEXT PRIMARY KEY,
		urls TEXT,
		status TEXT DEFAULT 'approved'
	);

	CREATE TABLE IF NOT EXISTS package_blacklist (
		pattern TEXT PRIMARY KEY,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);
	`

	_, err := db.Exec(schema)
	if err != nil {
		return err
	}

	// Initialize default stats
	statsKeys := []string{"requests_total", "cache_hits", "cache_misses", "bytes_served"}
	for _, key := range statsKeys {
		_, err := db.Exec("INSERT OR IGNORE INTO stats (key, value) VALUES (?, 0)", key)
		if err != nil {
			return err
		}
	}

	return seedDefaultMirrors()
}

func seedDefaultMirrors() error {
	// Check if mirrors table is empty
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM mirrors").Scan(&count)
	if err != nil {
		return err
	}

	if count > 0 {
		return nil
	}

	log := logger.Get()
	log.Info("Seeding default mirrors")

	defaultMirrors := map[string][]string{
		"debian": {
			"http://deb.debian.org/debian",
			"http://ftp.de.debian.org/debian",
			"http://cdn-fastly.deb.debian.org/debian",
			"http://ftp.us.debian.org/debian",
		},
		"debian-security": {
			"http://security.debian.org/debian-security",
			"http://deb.debian.org/debian-security",
		},
		"ubuntu": {
			"http://archive.ubuntu.com/ubuntu",
			"http://de.archive.ubuntu.com/ubuntu",
			"http://us.archive.ubuntu.com/ubuntu",
			"http://gb.archive.ubuntu.com/ubuntu",
		},
		"ubuntu-security": {
			"http://security.ubuntu.com/ubuntu",
		},
		"fedora": {
			"http://download.fedoraproject.org/pub/fedora/linux",
			"http://archives.fedoraproject.org/pub/fedora/linux",
		},
		"centos": {
			"http://mirror.centos.org/centos",
			"http://vault.centos.org/centos",
		},
		"rocky": {
			"http://download.rockylinux.org/pub/rocky",
			"http://rockylinux.map.fastly.net/pub/rocky",
		},
		"alma": {
			"http://repo.almalinux.org/almalinux",
		},
		"opensuse": {
			"http://download.opensuse.org/distribution",
			"http://download.opensuse.org/update",
			"http://download.opensuse.org/tumbleweed",
		},
		"kali": {
			"http://http.kali.org/kali",
			"http://kali.download/kali",
		},
		"archlinux": {
			"http://mirrors.kernel.org/archlinux",
			"http://mirror.rackspace.com/archlinux",
		},
		"alpine": {
			"http://dl-cdn.alpinelinux.org/alpine",
		},
		"raspbian": {
			"http://archive.raspbian.org/raspbian",
			"http://raspbian.raspberrypi.org/raspbian",
		},
		"docker": {
			"https://download.docker.com/linux",
		},
		"postgresql": {
			"http://apt.postgresql.org/pub/repos/apt",
		},
		"nodesource": {
			"http://deb.nodesource.com/node",
		},
		"jenkins": {
			"http://pkg.jenkins.io/debian",
			"http://pkg.jenkins.io/debian-stable",
		},
		"proxmox": {
			"http://download.proxmox.com/debian",
		},
		"nvidia": {
			"https://nvidia.github.io/libnvidia-container/stable/deb/amd64",
		},
		"hrfee": {
			"https://apt.hrfee.dev",
		},
	}

	for name, urls := range defaultMirrors {
		urlsJSON := `["` + urls[0]
		for i := 1; i < len(urls); i++ {
			urlsJSON += `","` + urls[i]
		}
		urlsJSON += `"]`

		_, err := db.Exec("INSERT INTO mirrors (name, urls, status) VALUES (?, ?, ?)",
			name, urlsJSON, "approved")
		if err != nil {
			return err
		}
	}

	return nil
}
