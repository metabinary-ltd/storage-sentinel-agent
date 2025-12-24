package collectors

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/metabinary-ltd/storagesentinel/internal/debug"
	"github.com/metabinary-ltd/storagesentinel/internal/storage"
)

type ZfsCollector struct {
	store  *storage.Store
	logger *slog.Logger
	zpool  string
	zfs    string
}

func NewZfsCollector(store *storage.Store, zpoolPath, zfsPath string, logger *slog.Logger) *ZfsCollector {
	return &ZfsCollector{store: store, zpool: zpoolPath, zfs: zfsPath, logger: logger}
}

// TriggerScrub starts a ZFS scrub on the specified pool
func (c *ZfsCollector) TriggerScrub(ctx context.Context, poolName string) error {
	ctx, cancel := ctxWithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := runCommand(ctx, c.zpool, "scrub", poolName)
	if err != nil {
		c.logger.Warn("zfs scrub trigger failed", "pool", poolName, "error", err)
		return err
	}

	c.logger.Info("zfs scrub started", "pool", poolName)
	return nil
}

func (c *ZfsCollector) Collect(ctx context.Context) error {
	// #region agent log
	debug.Log("internal/collectors/zfs.go:40", "ZfsCollector.Collect called", map[string]interface{}{
		"zpoolPath": c.zpool,
	})
	// #endregion
	ctx, cancel := ctxWithTimeout(ctx, 20*time.Second)
	defer cancel()

	// First get list of pools
	listOut, err := runCommand(ctx, c.zpool, "list", "-H", "-o", "name")
	// #region agent log
	debug.Log("internal/collectors/zfs.go:48", "zpool list result", map[string]interface{}{
		"output": strings.TrimSpace(listOut),
		"error":  fmt.Sprintf("%v", err),
	})
	// #endregion
	if err != nil {
		c.logger.Warn("zfs list failed", "error", err)
		return nil
	}

	poolNames := []string{}
	for _, line := range strings.Split(strings.TrimSpace(listOut), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) > 0 {
			poolNames = append(poolNames, parts[0])
		}
	}
	// #region agent log
	debug.Log("internal/collectors/zfs.go:63", "Parsed pool names", map[string]interface{}{
		"count": len(poolNames),
		"names": poolNames,
	})
	// #endregion

	// Get detailed status for each pool
	for _, poolName := range poolNames {
		c.collectPoolStatus(ctx, poolName)
	}

	return nil
}

func (c *ZfsCollector) collectPoolStatus(ctx context.Context, poolName string) {
	ctx, cancel := ctxWithTimeout(ctx, 20*time.Second)
	defer cancel()

	out, err := runCommand(ctx, c.zpool, "status", poolName)
	if err != nil {
		c.logger.Warn("zpool status failed", "pool", poolName, "error", err)
		return
	}

	// Parse pool state
	state := parsePoolState(out)
	
	// Parse scrub information
	lastScrubTime, lastScrubErrors := parseScrubInfo(out)
	
	// Check for active scrub
	if isScrubActive(out) {
		c.logger.Info("scrub in progress", "pool", poolName)
		// We could track active scrub progress here if needed
	}

	if err := c.store.UpsertPool(ctx, poolName, state, lastScrubTime, lastScrubErrors); err != nil {
		c.logger.Warn("failed to upsert pool", "pool", poolName, "error", err)
	}
}

func parsePoolState(output string) string {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "state:") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				return parts[1]
			}
		}
	}
	return "UNKNOWN"
}

func parseScrubInfo(output string) (int64, int64) {
	var lastScrubTime int64
	var lastScrubErrors int64

	// Look for the scan line
	lines := strings.Split(output, "\n")
	var scanLine string
	for _, line := range lines {
		if strings.Contains(strings.ToLower(line), "scan:") {
			scanLine = line
			break
		}
	}

	if scanLine == "" {
		return 0, 0
	}

	// Pattern 1: Completed scrub with date on same line
	// "scan: scrub repaired 0B in 0 days 00:00:00 with 0 errors on Mon Jan  1 00:00:00 2024"
	// Note: zpool status may have double spaces before single-digit days
	scrubCompletedRegex := regexp.MustCompile(`scan:\s+scrub.*?with\s+(\d+)\s+errors?\s+on\s+(.+)$`)
	matches := scrubCompletedRegex.FindStringSubmatch(scanLine)
	if len(matches) >= 3 {
		if errors, err := strconv.ParseInt(matches[1], 10, 64); err == nil {
			lastScrubErrors = errors
		}
		// Parse the date string - normalize multiple spaces
		dateStr := regexp.MustCompile(`\s+`).ReplaceAllString(strings.TrimSpace(matches[2]), " ")
		if t := parseScrubDate(dateStr); t > 0 {
			lastScrubTime = t
		}
		return lastScrubTime, lastScrubErrors
	}

	// Pattern 2: Completed scrub with date on next line or elsewhere
	// "scan: scrub repaired 0B in 0 days 00:00:00 with 0 errors"
	scrubSimpleRegex := regexp.MustCompile(`scan:\s+scrub.*?with\s+(\d+)\s+errors?`)
	matches = scrubSimpleRegex.FindStringSubmatch(scanLine)
	if len(matches) >= 2 {
		if errors, err := strconv.ParseInt(matches[1], 10, 64); err == nil {
			lastScrubErrors = errors
		}
		// Try to find date in the scan line or nearby lines
		if t := findScrubDateInContext(output, scanLine); t > 0 {
			lastScrubTime = t
		}
	}

	return lastScrubTime, lastScrubErrors
}

func parseScrubDate(dateStr string) int64 {
	// Try common date formats from zpool status
	// zpool status typically uses: "Mon Jan  1 00:00:00 2024" (note double space)
	formats := []string{
		"Mon Jan  2 15:04:05 2006",      // "Mon Jan  1 00:00:00 2024" (double space)
		"Mon Jan 2 15:04:05 2006",       // "Mon Jan 1 00:00:00 2024" (single space)
		time.RFC1123,                     // "Mon, 01 Jan 2024 00:00:00 GMT"
		"2006-01-02 15:04:05",           // "2024-01-01 00:00:00"
		"2006-01-02",                    // "2024-01-01"
		"Jan  2 15:04:05 2006",          // "Jan  1 00:00:00 2024" (without day name)
		"Jan 2 15:04:05 2006",           // "Jan 1 00:00:00 2024"
	}

	dateStr = strings.TrimSpace(dateStr)
	for _, format := range formats {
		if t, err := time.Parse(format, dateStr); err == nil {
			return t.Unix()
		}
	}

	return 0
}

func findScrubDateInContext(output, scanLine string) int64 {
	// First try to find date in the scan line itself (might be split)
	lines := strings.Split(output, "\n")
	for i, line := range lines {
		if strings.Contains(line, "scan:") {
			// Check current line
			if t := parseScrubDate(line); t > 0 {
				return t
			}
			// Check next few lines for date patterns
			for j := i + 1; j < len(lines) && j < i+3; j++ {
				nextLine := strings.TrimSpace(lines[j])
				// Skip empty lines and config sections
				if nextLine == "" || strings.HasPrefix(nextLine, "config:") || strings.HasPrefix(nextLine, "NAME") {
					break
				}
				if t := parseScrubDate(nextLine); t > 0 {
					return t
				}
			}
		}
	}
	return 0
}

func isScrubActive(output string) bool {
	outputLower := strings.ToLower(output)
	return strings.Contains(outputLower, "scan: scrub in progress") ||
		strings.Contains(outputLower, "scan: resilver in progress")
}
