package debug

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

var (
	debugEnabled bool
	debugLogPath string
	mu           sync.Mutex
)

// Init initializes debug logging with the given path and enabled state
func Init(logPath string, enabled bool) {
	mu.Lock()
	defer mu.Unlock()
	debugLogPath = logPath
	debugEnabled = enabled
}

// Log writes a debug log entry if debug logging is enabled
func Log(location, message string, data map[string]interface{}) {
	if !debugEnabled || debugLogPath == "" {
		return
	}

	mu.Lock()
	defer mu.Unlock()

	entry := map[string]interface{}{
		"location":  location,
		"message":   message,
		"data":      data,
		"timestamp": time.Now().UnixMilli(),
	}

	// Open file in append mode
	f, err := os.OpenFile(debugLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		// Silently fail - don't break the application if debug logging fails
		return
	}
	defer f.Close()

	// Write as NDJSON (one JSON object per line)
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(entry); err != nil {
		// Silently fail
		return
	}
}

// IsEnabled returns whether debug logging is enabled
func IsEnabled() bool {
	mu.Lock()
	defer mu.Unlock()
	return debugEnabled
}

// GetLogPath returns the current debug log path
func GetLogPath() string {
	mu.Lock()
	defer mu.Unlock()
	return debugLogPath
}

