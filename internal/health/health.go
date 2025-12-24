package health

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/metabinary-ltd/storagesentinel/internal/config"
	"github.com/metabinary-ltd/storagesentinel/internal/storage"
	"github.com/metabinary-ltd/storagesentinel/internal/types"
)

type Provider interface {
	Summary(ctx context.Context) (types.HealthReport, error)
}

type InMemoryProvider struct {
	logger *slog.Logger
}

func NewInMemoryProvider(logger *slog.Logger) *InMemoryProvider {
	return &InMemoryProvider{logger: logger}
}

func (p *InMemoryProvider) Summary(_ context.Context) (types.HealthReport, error) {
	p.logger.Debug("health summary requested (in-memory stub)")
	return types.HealthReport{
		Status: "ok",
		Disks:  []types.DiskHealth{},
		Pools:  []types.PoolHealth{},
	}, nil
}

type StorageBackedProvider struct {
	store        *storage.Store
	logger       *slog.Logger
	schedulingCfg config.SchedulingConfig
	alertsCfg    config.AlertsConfig
}

func NewStorageBackedProvider(store *storage.Store, logger *slog.Logger) *StorageBackedProvider {
	return &StorageBackedProvider{
		store:        store,
		logger:       logger,
		schedulingCfg: config.SchedulingConfig{}, // Default empty config
		alertsCfg:    config.AlertsConfig{},    // Default empty config
	}
}

func NewStorageBackedProviderWithConfig(store *storage.Store, schedulingCfg config.SchedulingConfig, logger *slog.Logger) *StorageBackedProvider {
	return &StorageBackedProvider{
		store:        store,
		logger:       logger,
		schedulingCfg: schedulingCfg,
		alertsCfg:    config.AlertsConfig{}, // Default empty config
	}
}

func NewStorageBackedProviderWithFullConfig(store *storage.Store, schedulingCfg config.SchedulingConfig, alertsCfg config.AlertsConfig, logger *slog.Logger) *StorageBackedProvider {
	return &StorageBackedProvider{
		store:        store,
		logger:       logger,
		schedulingCfg: schedulingCfg,
		alertsCfg:    alertsCfg,
	}
}

func (p *StorageBackedProvider) Summary(ctx context.Context) (types.HealthReport, error) {
	disks, err := p.store.ListDisks(ctx)
	if err != nil {
		return types.HealthReport{}, err
	}

	var dh []types.DiskHealth
	var alerts []types.Alert
	for _, d := range disks {
		diskHealth, diskAlerts := p.evaluateDisk(ctx, d)
		dh = append(dh, diskHealth)
		alerts = append(alerts, diskAlerts...)
	}

	pools, err := p.store.ListPools(ctx)
	if err != nil {
		return types.HealthReport{}, err
	}
	var ph []types.PoolHealth
	for _, pool := range pools {
		poolHealth, poolAlerts := p.evaluatePool(ctx, pool)
		ph = append(ph, poolHealth)
		alerts = append(alerts, poolAlerts...)
	}

	if err := p.persistAlerts(ctx, alerts); err != nil {
		p.logger.Warn("persist alerts", "error", err)
	}

	status := "ok"
	for _, a := range alerts {
		if a.Severity == "critical" {
			status = "critical"
			break
		}
		if a.Severity == "warning" && status == "ok" {
			status = "warning"
		}
	}

	return types.HealthReport{
		Status: status,
		Disks:  dh,
		Pools:  ph,
		Alerts: alerts,
	}, nil
}

func (p *StorageBackedProvider) evaluateDisk(ctx context.Context, d storage.Disk) (types.DiskHealth, []types.Alert) {
	health := types.DiskHealth{
		ID:          d.ID,
		Name:        d.Name,
		Type:        d.Type,
		Status:      "ok",
		HealthScore: 100,
	}
	var alerts []types.Alert

	if d.Type == "nvme" {
		health, alerts = p.evaluateNvmeDisk(ctx, d, health, alerts)
	} else {
		health, alerts = p.evaluateSmartDisk(ctx, d, health, alerts)
	}

	if health.HealthScore < 0 {
		health.HealthScore = 0
	}
	if health.HealthScore == 100 && len(health.Issues) == 0 {
		health.Status = "ok"
	}
	return health, alerts
}

func (p *StorageBackedProvider) evaluateSmartDisk(ctx context.Context, d storage.Disk, health types.DiskHealth, alerts []types.Alert) (types.DiskHealth, []types.Alert) {
	snap, _ := p.store.LatestSmart(ctx, d.ID)
	if snap == nil {
		return health, alerts
	}

	health.TemperatureC = snap.TemperatureC

	// Critical: SMART failed
	if snap.HealthStatus == "failed" {
		health.HealthScore = 10
		health.Status = "critical"
		health.Issues = append(health.Issues, "smart_failed")
		alerts = append(alerts, newAlert("critical", "disk", d.ID, "SMART FAILED", "SMART overall health failed"))
	}

	// Critical: Offline uncorrectable sectors
	if snap.OfflineUncorrect > 0 {
		health.HealthScore -= 40
		health.Status = "critical"
		health.Issues = append(health.Issues, "offline_uncorrectable")
		alerts = append(alerts, newAlert("critical", "disk", d.ID, "Offline uncorrectable sectors", 
			"Drive has uncorrectable sectors that cannot be recovered"))
	}

	// Warning: Pending sectors
	if snap.Pending > 0 {
		health.HealthScore -= 30
		health.Issues = append(health.Issues, "pending_sectors")
		alerts = append(alerts, newAlert("warning", "disk", d.ID, "Pending sectors", 
			"Drive has sectors waiting to be reallocated"))
	}

	// Warning: Reallocated sectors
	if snap.Reallocated > 0 {
		health.HealthScore -= 20
		health.Issues = append(health.Issues, "reallocated_sectors")
	}

	// Temperature warnings using configurable thresholds
	hddWarning := p.alertsCfg.TemperatureThresholds.HDDWarning
	if hddWarning == 0 {
		hddWarning = 55.0 // Default fallback
	}
	hddCritical := p.alertsCfg.TemperatureThresholds.HDDCritical
	if hddCritical == 0 {
		hddCritical = 70.0 // Default fallback
	}
	
	if snap.TemperatureC > hddCritical {
		health.HealthScore -= 30
		health.Status = "critical"
		health.Issues = append(health.Issues, "temperature_critical")
		alerts = append(alerts, newAlert("critical", "disk", d.ID, "Critical temperature", 
			"Drive temperature is above %.1f°C", hddCritical))
	} else if snap.TemperatureC > hddWarning {
		health.Issues = append(health.Issues, "temperature_high")
		alerts = append(alerts, newAlert("warning", "disk", d.ID, "High temperature", 
			"Drive temperature is above %.1f°C", hddWarning))
	}

	// Historical comparison
	history, _ := p.store.SmartHistory(ctx, d.ID, 2) // Get last 2 snapshots
	if len(history) >= 2 {
		prev := history[1] // Previous snapshot
		curr := history[0] // Current snapshot

		// Warning: Reallocated sectors increased
		if curr.Reallocated > prev.Reallocated {
			increase := curr.Reallocated - prev.Reallocated
			health.HealthScore -= 15
			health.Issues = append(health.Issues, "reallocated_increasing")
			alerts = append(alerts, newAlert("warning", "disk", d.ID, "Reallocated sectors increasing", 
				"Reallocated sectors increased by %d", increase))
		}

		// Warning: CRC errors increased significantly
		if curr.CRCErrors > prev.CRCErrors {
			increase := curr.CRCErrors - prev.CRCErrors
			if increase > 10 { // Significant increase
				health.Issues = append(health.Issues, "crc_errors_increasing")
				alerts = append(alerts, newAlert("warning", "disk", d.ID, "CRC errors increasing", 
					"CRC errors increased by %d (possible cable/connection issue)", increase))
			}
		}
	}

	// Info: CRC errors present but not increasing
	if snap.CRCErrors > 0 {
		health.Issues = append(health.Issues, "crc_errors")
	}

	if health.HealthScore < 60 && health.Status != "critical" {
		health.Status = "warning"
	}

	return health, alerts
}

func (p *StorageBackedProvider) evaluateNvmeDisk(ctx context.Context, d storage.Disk, health types.DiskHealth, alerts []types.Alert) (types.DiskHealth, []types.Alert) {
	snap, _ := p.store.LatestNvme(ctx, d.ID)
	if snap == nil {
		return health, alerts
	}

	health.TemperatureC = snap.TemperatureC

	// Temperature warnings using configurable thresholds
	nvmeWarning := p.alertsCfg.TemperatureThresholds.NvmeWarning
	if nvmeWarning == 0 {
		nvmeWarning = 70.0 // Default fallback
	}
	nvmeCritical := p.alertsCfg.TemperatureThresholds.NvmeCritical
	if nvmeCritical == 0 {
		nvmeCritical = 85.0 // Default fallback
	}
	
	if snap.TemperatureC > nvmeCritical {
		health.HealthScore -= 30
		health.Status = "critical"
		health.Issues = append(health.Issues, "temperature_critical")
		alerts = append(alerts, newAlert("critical", "disk", d.ID, "Critical temperature", 
			"Drive temperature is above %.1f°C", nvmeCritical))
	} else if snap.TemperatureC > nvmeWarning {
		health.Issues = append(health.Issues, "temperature_high")
		alerts = append(alerts, newAlert("warning", "disk", d.ID, "High temperature", 
			"Drive temperature is above %.1f°C", nvmeWarning))
	}

	// Critical: Wear level >= 95%
	if snap.PercentUsed >= 95 {
		health.HealthScore = 20
		health.Status = "critical"
		health.Issues = append(health.Issues, "nvme_wear_high")
		alerts = append(alerts, newAlert("critical", "disk", d.ID, "NVMe endurance high", "Percent used >=95"))
	} else if snap.PercentUsed >= 80 {
		health.HealthScore = 60
		health.Status = "warning"
		health.Issues = append(health.Issues, "nvme_wear_warning")
		alerts = append(alerts, newAlert("warning", "disk", d.ID, "NVMe endurance warning", "Percent used >=80"))
	}

	// Critical/Warning: Media errors
	if snap.MediaErrors > 0 {
		health.HealthScore -= 20
		health.Issues = append(health.Issues, "nvme_media_errors")
		if snap.MediaErrors > 10 {
			alerts = append(alerts, newAlert("critical", "disk", d.ID, "NVMe media errors", 
				"Drive has %d media errors", snap.MediaErrors))
		} else {
			alerts = append(alerts, newAlert("warning", "disk", d.ID, "NVMe media errors", 
				"Drive has %d media errors", snap.MediaErrors))
		}
	}

	// Parse and evaluate critical warning flags
	if snap.CriticalWarningFlags != "" {
		var flags struct {
			AvailableSpareLow            bool `json:"available_spare_low"`
			TemperatureThresholdExceeded bool `json:"temperature_threshold_exceeded"`
			ReliabilityDegraded           bool `json:"reliability_degraded"`
			ReadOnly                      bool `json:"read_only"`
		}
		if err := json.Unmarshal([]byte(snap.CriticalWarningFlags), &flags); err == nil {
			if flags.AvailableSpareLow {
				health.HealthScore -= 30
				health.Status = "critical"
				health.Issues = append(health.Issues, "nvme_spare_low")
				alerts = append(alerts, newAlert("critical", "disk", d.ID, "NVMe spare space low", 
					"Available spare space is below threshold"))
			}
			if flags.TemperatureThresholdExceeded {
				health.HealthScore -= 25
				health.Status = "critical"
				health.Issues = append(health.Issues, "nvme_temp_threshold")
				alerts = append(alerts, newAlert("critical", "disk", d.ID, "NVMe temperature threshold exceeded", 
					"Temperature is above or below threshold"))
			}
			if flags.ReliabilityDegraded {
				health.HealthScore -= 40
				health.Status = "critical"
				health.Issues = append(health.Issues, "nvme_reliability_degraded")
				alerts = append(alerts, newAlert("critical", "disk", d.ID, "NVMe reliability degraded", 
					"Device reliability is degraded"))
			}
			if flags.ReadOnly {
				health.HealthScore = 0
				health.Status = "critical"
				health.Issues = append(health.Issues, "nvme_read_only")
				alerts = append(alerts, newAlert("critical", "disk", d.ID, "NVMe read-only mode", 
					"Device has entered read-only mode"))
			}
		}
	}

	// Historical comparison: Unsafe shutdowns
	history, _ := p.store.NvmeHistory(ctx, d.ID, 2)
	if len(history) >= 2 {
		prev := history[1]
		curr := history[0]

		// Warning: Unsafe shutdowns increased
		if curr.UnsafeShutdowns > prev.UnsafeShutdowns {
			increase := curr.UnsafeShutdowns - prev.UnsafeShutdowns
			health.Issues = append(health.Issues, "unsafe_shutdowns_increased")
			alerts = append(alerts, newAlert("warning", "disk", d.ID, "Unsafe shutdowns increased", 
				"Unsafe shutdowns increased by %d", increase))
		}
	}

	if health.HealthScore < 60 && health.Status != "critical" {
		health.Status = "warning"
	}

	return health, alerts
}

func (p *StorageBackedProvider) evaluatePool(ctx context.Context, pool storage.PoolStatus) (types.PoolHealth, []types.Alert) {
	health := types.PoolHealth{
		Name:        pool.Name,
		State:       pool.State,
		Status:      "ok",
		HealthScore: 100,
	}
	var alerts []types.Alert

	// Critical: Pool not ONLINE
	if pool.State != "ONLINE" && pool.State != "" {
		health.Status = "critical"
		health.HealthScore = 0
		health.Issues = append(health.Issues, "pool_state_"+pool.State)
		alerts = append(alerts, newAlert("critical", "pool", pool.Name, "Pool not healthy", 
			"ZFS pool state: "+pool.State))
	}

	// Warning: Last scrub time older than interval
	if p.schedulingCfg.ZFSScrubInterval > 0 {
		lastScrubTime := int64(0)
		if pool.LastScrubTime.Valid {
			lastScrubTime = pool.LastScrubTime.Int64
		}

		if lastScrubTime > 0 {
			now := time.Now().Unix()
			intervalSeconds := int64(p.schedulingCfg.ZFSScrubInterval.Seconds())
			timeSinceScrub := now - lastScrubTime

			if timeSinceScrub > intervalSeconds {
				daysOverdue := (timeSinceScrub - intervalSeconds) / (24 * 3600)
				health.HealthScore -= 20
				health.Status = "warning"
				health.Issues = append(health.Issues, "scrub_overdue")
				alerts = append(alerts, newAlert("warning", "pool", pool.Name, "Scrub overdue", 
					"Last scrub was %d days ago (interval: %v)", daysOverdue, p.schedulingCfg.ZFSScrubInterval))
			}
		} else {
			// Never scrubbed
			health.Issues = append(health.Issues, "scrub_never")
			alerts = append(alerts, newAlert("warning", "pool", pool.Name, "Scrub never run", 
				"Pool has never been scrubbed"))
		}
	}

	// Warning/Critical: Last scrub had errors
	if pool.LastScrubError.Valid && pool.LastScrubError.Int64 > 0 {
		errors := pool.LastScrubError.Int64
		if errors > 100 {
			health.HealthScore -= 30
			health.Status = "critical"
			health.Issues = append(health.Issues, "scrub_errors_critical")
			alerts = append(alerts, newAlert("critical", "pool", pool.Name, "Scrub errors (critical)", 
				"Last scrub had %d errors", errors))
		} else {
			health.HealthScore -= 15
			health.Status = "warning"
			health.Issues = append(health.Issues, "scrub_errors")
			alerts = append(alerts, newAlert("warning", "pool", pool.Name, "Scrub errors", 
				"Last scrub had %d errors", errors))
		}
	}

	return health, alerts
}

func newAlert(sev, sourceType, sourceID, subject, msg string, args ...interface{}) types.Alert {
	message := msg
	if len(args) > 0 {
		message = fmt.Sprintf(msg, args...)
	}
	return types.Alert{
		Timestamp:  time.Now().Unix(),
		Severity:   sev,
		SourceType: sourceType,
		SourceID:   sourceID,
		Subject:    subject,
		Message:    message,
	}
}

func (p *StorageBackedProvider) persistAlerts(ctx context.Context, alerts []types.Alert) error {
	for _, a := range alerts {
		_, err := p.store.AddAlert(ctx, storage.Alert{
			Severity:   a.Severity,
			SourceType: a.SourceType,
			SourceID:   a.SourceID,
			Subject:    a.Subject,
			Message:    a.Message,
			Timestamp:  a.Timestamp,
		})
		if err != nil {
			return err
		}
	}
	return nil
}
