package collectors

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/metabinary-ltd/storagesentinel/internal/storage"
)

type NvmeCollector struct {
	store   *storage.Store
	logger  *slog.Logger
	binPath string
}

func NewNvmeCollector(store *storage.Store, binPath string, logger *slog.Logger) *NvmeCollector {
	return &NvmeCollector{store: store, binPath: binPath, logger: logger}
}

func (c *NvmeCollector) Collect(ctx context.Context, disks []storage.Disk) error {
	for _, d := range disks {
		if d.Type != "nvme" {
			continue
		}
		c.collectDisk(ctx, d)
	}
	return nil
}

func (c *NvmeCollector) collectDisk(ctx context.Context, disk storage.Disk) {
	ctx, cancel := ctxWithTimeout(ctx, 20*time.Second)
	defer cancel()

	out, err := runCommand(ctx, c.binPath, "smart-log", disk.Name)
	if err != nil {
		c.logger.Warn("nvme collect failed", "disk", disk.Name, "error", err)
		return
	}

	snap := storage.NvmeSnapshot{
		DiskID:    disk.ID,
		Timestamp: time.Now().Unix(),
	}
	for _, line := range strings.Split(out, "\n") {
		l := strings.ToLower(line)
		parseIntLine := func(prefix string, target *int64) {
			if strings.Contains(l, prefix) {
				fields := strings.Fields(line)
				if len(fields) > 0 {
					valStr := strings.TrimSuffix(fields[len(fields)-1], "%")
					if v, err := strconv.ParseInt(valStr, 10, 64); err == nil {
						*target = v
					}
				}
			}
		}
		parseFloatLine := func(prefix string, target *float64) {
			if strings.Contains(l, prefix) {
				fields := strings.Fields(line)
				if len(fields) > 0 {
					// Look for temperature value in any field (not just last)
					// Format examples:
					// "temperature : 54°C (327 Kelvin)"
					// "temperature: 45 C"
					for i, field := range fields {
						fieldLower := strings.ToLower(field)
						
						// Check for field containing "°C" or ending with "C" (but not "Celsius" or "Kelvin")
						if strings.Contains(field, "°C") || strings.Contains(field, "°c") {
							// Extract number from "54°C"
							valStr := strings.TrimSuffix(strings.TrimSuffix(field, "°C"), "°c")
					if v, err := strconv.ParseFloat(valStr, 64); err == nil {
						*target = v
								return
							}
						}
						
						// Check for Kelvin (K suffix, but not "ok" or "Kelvin)")
						if strings.HasSuffix(fieldLower, "k") && !strings.HasSuffix(fieldLower, "ok") && !strings.Contains(fieldLower, "kelvin") {
							valStr := strings.TrimSuffix(field, "K")
							if v, err := strconv.ParseFloat(valStr, 64); err == nil {
								// Convert Kelvin to Celsius: K - 273.15
								celsius := v - 273.15
								*target = celsius
								return
							}
						}
						
						// Check for Celsius (C suffix, but not "Celsius" or part of "°C")
						if strings.HasSuffix(fieldLower, "c") && !strings.Contains(fieldLower, "celsius") && !strings.Contains(field, "°") {
							valStr := strings.TrimSuffix(field, "C")
							if v, err := strconv.ParseFloat(valStr, 64); err == nil {
								// If value is > 200, it's likely Kelvin (room temp in K is ~293)
								if v > 200 {
									celsius := v - 273.15
									*target = celsius
								} else {
									*target = v
								}
								return
							}
						}
						
						// Try parsing as plain number (might be in a field like "54" before "°C" or "C")
						if v, err := strconv.ParseFloat(field, 64); err == nil {
							// Check if next field indicates unit
							if i+1 < len(fields) {
								nextField := strings.ToLower(fields[i+1])
								if strings.Contains(nextField, "kelvin") || nextField == "k" {
									celsius := v - 273.15
									*target = celsius
									return
								}
							}
							// If value > 200 and no unit specified, assume Kelvin
							if v > 200 {
								celsius := v - 273.15
								*target = celsius
								return
							}
						}
					}
				}
			}
		}

		parseIntLine("media errors", &snap.MediaErrors)
		parseIntLine("num err log entries", &snap.ErrorLogEntries)
		parseIntLine("unsafe shutdowns", &snap.UnsafeShutdowns)
		parseIntLine("power on hours", &snap.PowerOnHours)
		parseIntLine("data units written", &snap.DataWrittenBytes)
		parseIntLine("data units read", &snap.DataReadBytes)
		if strings.Contains(l, "percentage used") {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				valStr := strings.TrimSuffix(fields[len(fields)-1], "%")
				if v, err := strconv.ParseFloat(valStr, 64); err == nil {
					snap.PercentUsed = v
				}
			}
		}
		parseFloatLine("temperature", &snap.TemperatureC)
	}

	// Parse critical warnings
	snap.CriticalWarningFlags = parseCriticalWarnings(out)

	// Store raw output
	snap.RawOutput = out

	if err := c.store.AddNvmeSnapshot(ctx, snap); err != nil {
		c.logger.Warn("failed to store nvme snapshot", "disk", disk.Name, "error", err)
	}
}

// CriticalWarningFlags represents the structured critical warning flags
type CriticalWarningFlags struct {
	AvailableSpareLow              bool `json:"available_spare_low"`
	TemperatureThresholdExceeded   bool `json:"temperature_threshold_exceeded"`
	ReliabilityDegraded            bool `json:"reliability_degraded"`
	ReadOnly                       bool `json:"read_only"`
}

// parseCriticalWarnings parses critical warnings from nvme smart-log output
// and returns them as a JSON string
func parseCriticalWarnings(output string) string {
	flags := CriticalWarningFlags{}
	outputLower := strings.ToLower(output)
	
	// Parse from hex value format: "critical_warning: 0x01" or "critical warning: 0x01"
	hexValue := extractHexValue(output, "critical")
	if hexValue >= 0 {
		// Use hex value - this is the authoritative source
		flags.AvailableSpareLow = (hexValue & 0x01) != 0
		flags.TemperatureThresholdExceeded = (hexValue & 0x02) != 0
		flags.ReliabilityDegraded = (hexValue & 0x04) != 0
		flags.ReadOnly = (hexValue & 0x08) != 0
	} else {
		// Fallback: Parse from text format (only if hex parsing failed)
		// Be very conservative - only flag if there's clear evidence of an actual problem
		
		// Check if critical_warning is explicitly 0 - if so, all flags are false
		if strings.Contains(outputLower, "critical_warning") || strings.Contains(outputLower, "critical warning") {
			// Look for ": 0" after critical_warning
			lines := strings.Split(output, "\n")
			for _, line := range lines {
				lineLower := strings.ToLower(line)
				if (strings.Contains(lineLower, "critical_warning") || strings.Contains(lineLower, "critical warning")) &&
					strings.Contains(lineLower, ": 0") {
					// critical_warning is 0, so all flags are false
					flags.AvailableSpareLow = false
					flags.TemperatureThresholdExceeded = false
					flags.ReliabilityDegraded = false
					flags.ReadOnly = false
					break
				}
			}
		}
		
		// Only set flags to true if we didn't find critical_warning: 0 AND there's clear evidence
		if !strings.Contains(outputLower, "critical_warning") && !strings.Contains(outputLower, "critical warning") {
			// No critical_warning field found, use conservative text parsing
		flags.AvailableSpareLow = strings.Contains(outputLower, "available spare") &&
				(strings.Contains(outputLower, "below") || strings.Contains(outputLower, "low")) &&
				!strings.Contains(outputLower, "available_spare_threshold")
			
			// Only flag temperature threshold if explicitly mentioned as exceeded/warning (not just field names)
			flags.TemperatureThresholdExceeded = (strings.Contains(outputLower, "temperature") &&
				strings.Contains(outputLower, "exceeded")) &&
				!strings.Contains(outputLower, "warning temperature time") &&
				!strings.Contains(outputLower, "critical composite temperature time")
			
		flags.ReliabilityDegraded = strings.Contains(outputLower, "reliability") &&
			strings.Contains(outputLower, "degraded")
		flags.ReadOnly = strings.Contains(outputLower, "read only") || strings.Contains(outputLower, "read-only")
		}
	}
	
	// Marshal to JSON
	jsonBytes, err := json.Marshal(flags)
	if err != nil {
		return "{}"
	}
	return string(jsonBytes)
}

// extractHexValue extracts a hex value from a line containing the given keyword
func extractHexValue(output, keyword string) int64 {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		lineLower := strings.ToLower(line)
		// Match "critical_warning" or "critical warning" (with or without underscore)
		if (strings.Contains(lineLower, "critical_warning") || 
			(strings.Contains(lineLower, keyword) && strings.Contains(lineLower, "warning"))) {
			// Look for hex pattern: 0xXX or 0XXX
			fields := strings.Fields(line)
			for _, field := range fields {
				fieldLower := strings.ToLower(field)
				if strings.HasPrefix(fieldLower, "0x") {
					field = strings.TrimPrefix(fieldLower, "0x")
					if val, err := strconv.ParseInt(field, 16, 64); err == nil {
						return val
					}
				}
			}
			// Check for decimal value after ":" (format: "critical_warning: 0")
			for i, field := range fields {
				if field == ":" && i+1 < len(fields) {
					if val, err := strconv.ParseInt(fields[i+1], 10, 64); err == nil {
						return val
					}
				}
				// Also check if field itself is a number (might be right after colon with no space)
				if strings.HasSuffix(field, ":") && i+1 < len(fields) {
					if val, err := strconv.ParseInt(fields[i+1], 10, 64); err == nil {
						return val
					}
				}
			}
		}
	}
	return -1
}
