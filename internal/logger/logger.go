package logger

import (
	"os"
	"sync"

	"github.com/sirupsen/logrus"
)

var (
	log  *logrus.Logger
	once sync.Once
)

// Init initializes the logger
func Init() {
	once.Do(func() {
		log = logrus.New()
		log.SetOutput(os.Stdout)
		log.SetFormatter(&logrus.TextFormatter{
			FullTimestamp:   true,
			TimestampFormat: "2006-01-02 15:04:05",
		})
		log.SetLevel(logrus.InfoLevel)
	})
}

// Get returns the logger instance
func Get() *logrus.Logger {
	if log == nil {
		Init()
	}
	return log
}

// SetLevel sets the log level
func SetLevel(level string) {
	l := Get()
	switch level {
	case "DEBUG":
		l.SetLevel(logrus.DebugLevel)
	case "INFO":
		l.SetLevel(logrus.InfoLevel)
	case "WARN", "WARNING":
		l.SetLevel(logrus.WarnLevel)
	case "ERROR":
		l.SetLevel(logrus.ErrorLevel)
	default:
		l.SetLevel(logrus.InfoLevel)
	}
}
