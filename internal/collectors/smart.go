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

type SmartCollector struct {
	store   *storage.Store
	logger  *slog.Logger
	binPath string
}

func NewSmartCollector(store *storage.Store, binPath string, logger *slog.Logger) *SmartCollector {
	return &SmartCollector{store: store, binPath: binPath, logger: logger}
}

func (c *SmartCollector) Collect(ctx context.Context, disks []storage.Disk) error {
	for _, d := range disks {
		if d.Type == "nvme" {
			continue
		}
		c.collectDisk(ctx, d)
	}
	return nil
}

// RunTest triggers a SMART self-test on a disk
// testType should be "short" or "long"
func (c *SmartCollector) RunTest(ctx context.Context, disk storage.Disk, testType string) error {
	ctx, cancel := ctxWithTimeout(ctx, 30*time.Second)
	defer cancel()

	// smartctl -t short /dev/sdX or smartctl -t long /dev/sdX
	_, err := runCommand(ctx, c.binPath, "-t", testType, disk.Name)
	if err != nil {
		c.logger.Warn("smart test failed", "disk", disk.Name, "test", testType, "error", err)
		return err
	}

	c.logger.Info("smart test started", "disk", disk.Name, "test", testType)
	return nil
}

func (c *SmartCollector) collectDisk(ctx context.Context, disk storage.Disk) {
	ctx, cancel := ctxWithTimeout(ctx, 20*time.Second)
	defer cancel()

	out, err := runCommand(ctx, c.binPath, "-H", "-A", disk.Name)
	if err != nil {
		c.logger.Warn("smart collect failed", "disk", disk.Name, "error", err)
		return
	}

	snap := storage.SmartSnapshot{
		DiskID:    disk.ID,
		Timestamp: time.Now().Unix(),
	}
	if strings.Contains(out, "PASSED") {
		snap.HealthStatus = "passed"
	} else if strings.Contains(strings.ToUpper(out), "FAILED") {
		snap.HealthStatus = "failed"
	} else {
		snap.HealthStatus = "unknown"
	}

	parseTable(out, map[string]*int64{
		"Reallocated_Sector_Ct":  &snap.Reallocated,
		"Current_Pending_Sector": &snap.Pending,
		"Offline_Uncorrectable":  &snap.OfflineUncorrect,
		"UDMA_CRC_Error_Count":   &snap.CRCErrors,
		"Power_On_Hours":         &snap.PowerOnHours,
		"Spin_Retry_Count":       &snap.SpinRetryCount,
		"Load_Cycle_Count":       &snap.LoadCycleCount,
	})
	if temp := parseTemperature(out); temp != nil {
		snap.TemperatureC = *temp
	}

	// Store full SMART output as JSON
	if rawJSON, err := json.Marshal(out); err == nil {
		snap.RawJSON = string(rawJSON)
	}

	if err := c.store.AddSmartSnapshot(ctx, snap); err != nil {
		c.logger.Warn("failed to store smart snapshot", "disk", disk.Name, "error", err)
	}
}

func parseTable(out string, fields map[string]*int64) {
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		for key, ref := range fields {
			if strings.Contains(line, key) {
				parts := strings.Fields(line)
				if len(parts) >= 10 {
					if v, err := strconv.ParseInt(parts[9], 10, 64); err == nil {
						*ref = v
					}
				}
			}
		}
	}
}

func parseTemperature(out string) *float64 {
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		if strings.Contains(strings.ToLower(line), "temperature") {
			fields := strings.Fields(line)
			// Check if this is a SMART attribute table line (has Temperature in attribute name and enough fields for RAW_VALUE)
			// Format: "194 Temperature_Celsius     0x0002   026   026   000    Old_age   Always       -       49 (Min/Max 19/59)"
			// RAW_VALUE is typically the 9th field (index 9)
			if len(fields) >= 10 && (strings.Contains(strings.ToLower(fields[1]), "temperature") || strings.Contains(strings.ToLower(line), "temperature_celsius")) {
				// Try to parse RAW_VALUE (9th field, index 9)
				if v, err := strconv.ParseFloat(fields[9], 64); err == nil {
					// Check if value is > 100, might be Fahrenheit
					if v > 100 {
						// Convert Fahrenheit to Celsius: (F - 32) * 5/9
						celsius := (v - 32) * 5.0 / 9.0
						return &celsius
					}
					// Assume Celsius for values <= 100
					return &v
				}
			}
			
			// Fallback: Try to parse fields with unit suffixes (for non-table formats)
			for i, field := range fields {
				fieldLower := strings.ToLower(field)
				// Check for Fahrenheit (F suffix or next field is F/Fahrenheit)
				if strings.HasSuffix(fieldLower, "f") && !strings.HasSuffix(fieldLower, "of") {
					// Remove F suffix and parse
					if v, err := strconv.ParseFloat(strings.TrimSuffix(field, "F"), 64); err == nil {
						// Convert Fahrenheit to Celsius: (F - 32) * 5/9
						celsius := (v - 32) * 5.0 / 9.0
						return &celsius
					}
				}
				// Check if next field is F or Fahrenheit
				if i+1 < len(fields) {
					nextField := strings.ToLower(fields[i+1])
					if nextField == "f" || nextField == "fahrenheit" {
						if v, err := strconv.ParseFloat(field, 64); err == nil {
							// Convert Fahrenheit to Celsius
							celsius := (v - 32) * 5.0 / 9.0
							return &celsius
						}
					}
				}
				// Check for Celsius (C suffix or next field is C/Celsius)
				if strings.HasSuffix(fieldLower, "c") && !strings.HasSuffix(fieldLower, "nc") && !strings.HasSuffix(fieldLower, "ic") {
					if v, err := strconv.ParseFloat(strings.TrimSuffix(field, "C"), 64); err == nil {
						return &v
					}
				}
				// Check if next field is C or Celsius
				if i+1 < len(fields) {
					nextField := strings.ToLower(fields[i+1])
					if nextField == "c" || nextField == "celsius" {
						if v, err := strconv.ParseFloat(field, 64); err == nil {
							return &v
						}
					}
				}
			}
		}
	}
	return nil
}
