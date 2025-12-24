package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/metabinary-ltd/storagesentinel/internal/collectors"
	"github.com/metabinary-ltd/storagesentinel/internal/config"
	"github.com/metabinary-ltd/storagesentinel/internal/discovery"
	"github.com/metabinary-ltd/storagesentinel/internal/health"
	"github.com/metabinary-ltd/storagesentinel/internal/notifier"
	"github.com/metabinary-ltd/storagesentinel/internal/storage"
	"github.com/metabinary-ltd/storagesentinel/internal/types"
	"github.com/metabinary-ltd/storagesentinel/internal/uplink"
)

type Scheduler struct {
	logger       *slog.Logger
	discovery    *discovery.Service
	store        *storage.Store
	cfg          config.SchedulingConfig
	cloudCfg     config.CloudConfig
	smart        *collectors.SmartCollector
	nvme         *collectors.NvmeCollector
	zfs          *collectors.ZfsCollector
	health       health.Provider
	notifier     *notifier.Notifier
	uplink       *uplink.Client
	commandQueue chan uplink.Command
}

func New(logger *slog.Logger, cfg config.SchedulingConfig, cloudCfg config.CloudConfig, store *storage.Store, discovery *discovery.Service, smart *collectors.SmartCollector, nvme *collectors.NvmeCollector, zfs *collectors.ZfsCollector, health health.Provider, notifier *notifier.Notifier, uplinkClient *uplink.Client) *Scheduler {
	commandQueue := make(chan uplink.Command, 10)
	return &Scheduler{
		logger:       logger,
		cfg:          cfg,
		cloudCfg:     cloudCfg,
		store:        store,
		discovery:    discovery,
		smart:        smart,
		nvme:         nvme,
		zfs:          zfs,
		health:       health,
		notifier:     notifier,
		uplink:       uplinkClient,
		commandQueue: commandQueue,
	}
}

func (s *Scheduler) Start(ctx context.Context, once bool) {
	if once {
		s.logger.Info("scheduler once mode - running discovery and collectors")
		s.runOnce(ctx)
		return
	}

	s.logger.Info("scheduler started")
	
	// Poll and store cloud schedules on startup if cloud is enabled
	if s.uplink != nil && s.cloudCfg.Enabled {
		s.pollAndStoreSchedules(ctx)
	}
	
	// Run discovery immediately on startup
	if s.discovery != nil {
		_ = s.discovery.RunOnce(ctx)
	}
	
	// Run discovery periodically (every 6 hours by default)
	go s.runLoop(ctx, 6*time.Hour, s.runDiscoveryLoop)
	go s.runLoopWithSchedule(ctx, "ZFS_STATUS", s.cfg.ZFSStatusInterval, s.runZfsLoop)
	go s.runLoopWithSchedule(ctx, "SMART_COLLECT", s.cfg.SmartCollectInterval, s.runSmartLoop)
	go s.runLoopWithSchedule(ctx, "NVME_COLLECT", s.cfg.SmartCollectInterval, s.runNvmeLoop)
	
	// Run SMART test schedulers if intervals are configured
	if s.cfg.SmartShortInterval > 0 {
		go s.runLoopWithSchedule(ctx, "SMART_SHORT_TEST", s.cfg.SmartShortInterval, func(ctx context.Context) {
			effectiveInterval := s.getEffectiveInterval(ctx, "SMART_SHORT_TEST", s.cfg.SmartShortInterval)
			s.runSmartTestsScheduler(ctx, "short", effectiveInterval)
		})
	}
	if s.cfg.SmartLongInterval > 0 {
		go s.runLoopWithSchedule(ctx, "SMART_LONG_TEST", s.cfg.SmartLongInterval, func(ctx context.Context) {
			effectiveInterval := s.getEffectiveInterval(ctx, "SMART_LONG_TEST", s.cfg.SmartLongInterval)
			s.runSmartTestsScheduler(ctx, "long", effectiveInterval)
		})
	}
	
	// Run ZFS scrub scheduler if interval is configured
	if s.cfg.ZFSScrubInterval > 0 {
		go s.runLoopWithSchedule(ctx, "ZFS_SCRUB", s.cfg.ZFSScrubInterval, s.runZfsScrubScheduler)
	}
	
	go s.runLoop(ctx, 24*time.Hour, s.runPruneLoop)
	
	// Cloud upload and command polling if enabled
	if s.uplink != nil && s.cloudCfg.Enabled {
		uploadInterval := s.cloudCfg.UploadInterval
		if uploadInterval <= 0 {
			uploadInterval = 15 * time.Minute
		}
		go s.runLoop(ctx, uploadInterval, s.runCloudUploadLoop)
		
		pollInterval := s.cloudCfg.CommandPollInterval
		if pollInterval <= 0 {
			pollInterval = 5 * time.Minute
		}
		go s.runLoop(ctx, pollInterval, s.runCommandPollLoop)
		go s.runCommandProcessor(ctx)
		
		// Poll schedules periodically (every hour)
		go s.runLoop(ctx, 1*time.Hour, s.pollAndStoreSchedules)
	}
	
	<-ctx.Done()
	s.logger.Info("scheduler stopping")
}

func (s *Scheduler) runOnce(ctx context.Context) {
	if s.discovery != nil {
		_ = s.discovery.RunOnce(ctx)
	}
	disks, _ := s.store.ListDisks(ctx)
	if s.smart != nil {
		_ = s.smart.Collect(ctx, disks)
	}
	if s.nvme != nil {
		_ = s.nvme.Collect(ctx, disks)
	}
	if s.zfs != nil {
		_ = s.zfs.Collect(ctx)
	}
	s.dispatchHealth(ctx)
}

func (s *Scheduler) runLoop(ctx context.Context, interval time.Duration, fn func(context.Context)) {
	if interval <= 0 {
		interval = time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		fn(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// runLoopWithSchedule runs a loop that checks both config and cloud schedules
func (s *Scheduler) runLoopWithSchedule(ctx context.Context, taskType string, configInterval time.Duration, fn func(context.Context)) {
	// Start with config interval
	interval := configInterval
	if interval <= 0 {
		interval = time.Hour
	}
	
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	
	for {
		// Check for cloud schedule and use the most frequent (shortest interval)
		effectiveInterval := s.getEffectiveInterval(ctx, taskType, configInterval)
		if effectiveInterval != interval {
			interval = effectiveInterval
			ticker.Stop()
			ticker = time.NewTicker(interval)
		}
		
		fn(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// getEffectiveInterval returns the most frequent interval (shortest duration) between config and cloud schedule
func (s *Scheduler) getEffectiveInterval(ctx context.Context, taskType string, configInterval time.Duration) time.Duration {
	cloudSchedule, err := s.store.GetScheduleForTask(ctx, taskType)
	if err != nil || cloudSchedule == nil || !cloudSchedule.Enabled {
		return configInterval
	}
	
	// Parse cloud schedule to get interval
	var cloudInterval time.Duration
	if cloudSchedule.ScheduleType == "INTERVAL" {
		cloudInterval, err = ParseInterval(cloudSchedule.ScheduleValue)
		if err != nil {
			s.logger.Warn("failed to parse cloud schedule interval", "task", taskType, "value", cloudSchedule.ScheduleValue, "error", err)
			return configInterval
		}
	} else if cloudSchedule.ScheduleType == "CRON" {
		// For cron, we need to calculate next execution time
		// For simplicity, we'll use a check interval (e.g., every minute) and check if it's time
		// A more sophisticated approach would calculate the actual next time
		cloudInterval = 1 * time.Minute // Check every minute for cron schedules
	} else {
		return configInterval
	}
	
	// Return the shorter interval (more frequent)
	if cloudInterval < configInterval {
		return cloudInterval
	}
	return configInterval
}

// pollAndStoreSchedules polls schedules from cloud and stores them locally
func (s *Scheduler) pollAndStoreSchedules(ctx context.Context) {
	if s.uplink == nil || !s.cloudCfg.Enabled {
		return
	}
	
	schedules, err := s.uplink.PollSchedules(ctx)
	if err != nil {
		s.logger.Warn("failed to poll schedules from cloud", "error", err)
		return
	}
	
	// Convert to storage format
	cloudSchedules := make([]storage.CloudSchedule, 0, len(schedules))
	for _, sched := range schedules {
		cloudSchedules = append(cloudSchedules, storage.CloudSchedule{
			ID:            sched.ID,
			TaskType:      sched.TaskType,
			ScheduleType:  sched.ScheduleType,
			ScheduleValue: sched.ScheduleValue,
			Enabled:       sched.Enabled,
			UpdatedAt:     sched.UpdatedAt,
		})
	}
	
	if err := s.store.StoreSchedules(ctx, cloudSchedules); err != nil {
		s.logger.Warn("failed to store schedules", "error", err)
		return
	}
	
	if len(cloudSchedules) > 0 {
		s.logger.Info("stored cloud schedules", "count", len(cloudSchedules))
	}
}

func (s *Scheduler) runSmartLoop(ctx context.Context) {
	disks, _ := s.store.ListDisks(ctx)
	if s.smart != nil {
		if err := s.smart.Collect(ctx, disks); err != nil {
			s.logger.Warn("smart loop error", "error", err)
		}
	}
	s.dispatchHealth(ctx)
}

func (s *Scheduler) runNvmeLoop(ctx context.Context) {
	disks, _ := s.store.ListDisks(ctx)
	if s.nvme != nil {
		if err := s.nvme.Collect(ctx, disks); err != nil {
			s.logger.Warn("nvme loop error", "error", err)
		}
	}
	s.dispatchHealth(ctx)
}

func (s *Scheduler) runZfsLoop(ctx context.Context) {
	if s.zfs != nil {
		if err := s.zfs.Collect(ctx); err != nil {
			s.logger.Warn("zfs loop error", "error", err)
		}
	}
	s.dispatchHealth(ctx)
}

func (s *Scheduler) runDiscoveryLoop(ctx context.Context) {
	if s.discovery != nil {
		if err := s.discovery.RunOnce(ctx); err != nil {
			s.logger.Warn("discovery loop error", "error", err)
		}
	}
}

func (s *Scheduler) runSmartTestsScheduler(ctx context.Context, testType string, interval time.Duration) {
	if s.smart == nil || s.store == nil {
		return
	}

	disks, err := s.store.ListDisks(ctx)
	if err != nil {
		s.logger.Warn("failed to list disks for smart test scheduler", "error", err)
		return
	}

	now := time.Now().Unix()
	intervalSeconds := int64(interval.Seconds())

	for _, disk := range disks {
		if disk.Type == "nvme" {
			continue // SMART tests are for SATA/SAS drives only
		}

		lastTest, err := s.store.GetLastSmartTestTime(ctx, disk.ID, testType)
		if err != nil {
			s.logger.Warn("failed to get last smart test time", "disk", disk.Name, "test", testType, "error", err)
			continue
		}

		// If never tested or interval has elapsed, trigger test
		if lastTest == 0 || (now-lastTest) >= intervalSeconds {
			if err := s.smart.RunTest(ctx, disk, testType); err == nil {
				_ = s.store.RecordSmartTest(ctx, disk.ID, testType)
				s.logger.Info("scheduled smart test", "disk", disk.Name, "test", testType)
			}
		}
	}
}

func (s *Scheduler) runZfsScrubScheduler(ctx context.Context) {
	if s.zfs == nil || s.store == nil {
		return
	}

	pools, err := s.store.ListPools(ctx)
	if err != nil {
		s.logger.Warn("failed to list pools for scrub scheduler", "error", err)
		return
	}

	now := time.Now().Unix()
	effectiveInterval := s.getEffectiveInterval(ctx, "ZFS_SCRUB", s.cfg.ZFSScrubInterval)
	intervalSeconds := int64(effectiveInterval.Seconds())

	for _, pool := range pools {
		lastScrub, err := s.store.GetLastScrubTime(ctx, pool.Name)
		if err != nil {
			s.logger.Warn("failed to get last scrub time", "pool", pool.Name, "error", err)
			continue
		}

		// Check cloud schedule if available
		cloudSchedule, _ := s.store.GetScheduleForTask(ctx, "ZFS_SCRUB")
		shouldRun := false
		
		if cloudSchedule != nil && cloudSchedule.Enabled {
			// Check if it's time based on cloud schedule
			if cloudSchedule.ScheduleType == "CRON" {
				nextTime, err := NextCronTime(cloudSchedule.ScheduleValue, time.Unix(lastScrub, 0))
				if err == nil && time.Now().After(nextTime) {
					shouldRun = true
				}
			} else {
				// INTERVAL schedule - use interval check
				if lastScrub == 0 || (now-lastScrub) >= intervalSeconds {
					shouldRun = true
				}
			}
		} else {
			// Use config interval
			if lastScrub == 0 || (now-lastScrub) >= intervalSeconds {
				shouldRun = true
			}
		}

		if shouldRun {
			if err := s.zfs.TriggerScrub(ctx, pool.Name); err == nil {
				// Record scrub start in history
				_ = s.store.AddScrubHistory(ctx, storage.ScrubHistoryEntry{
					PoolName:  pool.Name,
					StartTime: now,
					EndTime:   0, // Will be updated when scrub completes
					Errors:    0,
					Notes:     "Scheduled scrub",
				})
				s.logger.Info("scheduled zfs scrub", "pool", pool.Name)
			}
		}
	}
}

func (s *Scheduler) runPruneLoop(ctx context.Context) {
	if s.store != nil {
		if err := s.store.PruneOldSnapshots(ctx, 90); err != nil {
			s.logger.Warn("prune snapshots failed", "error", err)
		}
	}
}

func (s *Scheduler) dispatchHealth(ctx context.Context) {
	if s.health == nil {
		return
	}
	report, err := s.health.Summary(ctx)
	if err == nil && s.notifier != nil {
		s.notifier.Send(ctx, report.Alerts)
	}
	if err == nil && s.uplink != nil {
		_ = s.uplink.SendSummary(ctx, report)
	}
}

func (s *Scheduler) runCloudUploadLoop(ctx context.Context) {
	if s.uplink == nil || !s.cloudCfg.Enabled {
		return
	}

	// Get current disks and pools
	disks, err := s.store.ListDisks(ctx)
	if err != nil {
		s.logger.Warn("failed to list disks for cloud upload", "error", err)
		return
	}

	pools, err := s.store.ListPools(ctx)
	if err != nil {
		s.logger.Warn("failed to list pools for cloud upload", "error", err)
		return
	}

	// Get latest snapshots for each disk
	var smartSnaps []types.SmartSnapshot
	var nvmeSnaps []types.NvmeSnapshot
	for _, disk := range disks {
		if disk.Type == "nvme" {
			hist, _ := s.store.NvmeHistory(ctx, disk.ID, 1)
			if len(hist) > 0 {
				snap := hist[0]
				nvmeSnaps = append(nvmeSnaps, types.NvmeSnapshot{
					DiskID:             snap.DiskID,
					PercentUsed:        snap.PercentUsed,
					MediaErrors:        snap.MediaErrors,
					ErrorLogEntries:    snap.ErrorLogEntries,
					PowerOnHours:       snap.PowerOnHours,
					UnsafeShutdowns:    snap.UnsafeShutdowns,
					TemperatureC:       snap.TemperatureC,
					DataWrittenBytes:   snap.DataWrittenBytes,
					DataReadBytes:      snap.DataReadBytes,
					TimestampUnixMilli: snap.Timestamp * 1000,
				})
			}
		} else {
			hist, _ := s.store.SmartHistory(ctx, disk.ID, 1)
			if len(hist) > 0 {
				snap := hist[0]
				smartSnaps = append(smartSnaps, types.SmartSnapshot{
					DiskID:             snap.DiskID,
					HealthStatus:       snap.HealthStatus,
					Reallocated:        snap.Reallocated,
					Pending:            snap.Pending,
					OfflineUncorrect:   snap.OfflineUncorrect,
					CRCErrors:          snap.CRCErrors,
					TemperatureC:       snap.TemperatureC,
					PowerOnHours:        snap.PowerOnHours,
					TimestampUnixMilli: snap.Timestamp * 1000,
				})
			}
		}
	}

	// Convert pools to types
	var poolStatuses []types.PoolStatus
	for _, pool := range pools {
		var lastScrubTime int64
		if pool.LastScrubTime.Valid {
			lastScrubTime = pool.LastScrubTime.Int64
		}
		var lastScrubErrors int64
		if pool.LastScrubError.Valid {
			lastScrubErrors = pool.LastScrubError.Int64
		}
		poolStatuses = append(poolStatuses, types.PoolStatus{
			PoolName:          pool.Name,
			State:             pool.State,
			LastScrubTimeUnix: lastScrubTime,
			LastScrubErrors:   lastScrubErrors,
		})
	}

	// Get health report
	report, err := s.health.Summary(ctx)
	if err != nil {
		s.logger.Warn("failed to get health report for cloud upload", "error", err)
		return
	}

	// Convert disks to types
	var diskTypes []types.Disk
	for _, d := range disks {
		diskTypes = append(diskTypes, types.Disk{
			ID:        d.ID,
			Name:      d.Name,
			Type:      d.Type,
			Model:     d.Model,
			Serial:    d.Serial,
			Firmware:  d.Firmware,
			SizeBytes: d.SizeBytes,
		})
	}

	payload := uplink.SnapshotPayload{
		Timestamp:    time.Now().Unix(),
		Disks:        diskTypes,
		Pools:        poolStatuses,
		SmartSnaps:   smartSnaps,
		NvmeSnaps:    nvmeSnaps,
		HealthReport: &report,
	}

	if err := s.uplink.SendFullSnapshot(ctx, payload); err != nil {
		s.logger.Warn("failed to upload snapshot to cloud", "error", err)
	} else {
		s.logger.Debug("uploaded snapshot to cloud")
	}
}

func (s *Scheduler) runCommandPollLoop(ctx context.Context) {
	if s.uplink == nil || !s.cloudCfg.Enabled {
		return
	}

	commands, err := s.uplink.PollCommands(ctx)
	if err != nil {
		s.logger.Warn("failed to poll commands from cloud", "error", err)
		return
	}

	for _, cmd := range commands {
		select {
		case s.commandQueue <- cmd:
		default:
			s.logger.Warn("command queue full, dropping command", "id", cmd.ID)
		}
	}
}

func (s *Scheduler) runCommandProcessor(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case cmd := <-s.commandQueue:
			s.processCommand(ctx, cmd)
		}
	}
}

func (s *Scheduler) processCommand(ctx context.Context, cmd uplink.Command) {
	var success bool
	var errorMsg string

	switch cmd.Type {
	case "trigger_scrub":
		var params struct {
			PoolName string `json:"pool_name"`
		}
		if err := json.Unmarshal(cmd.Params, &params); err != nil {
			errorMsg = fmt.Sprintf("invalid params: %v", err)
			break
		}
		if s.zfs != nil {
			if err := s.zfs.TriggerScrub(ctx, params.PoolName); err != nil {
				errorMsg = err.Error()
			} else {
				success = true
				s.logger.Info("executed remote scrub command", "pool", params.PoolName, "cmd_id", cmd.ID)
			}
		} else {
			errorMsg = "ZFS collector not available"
		}

	case "collect_smart":
		disks, err := s.store.ListDisks(ctx)
		if err != nil {
			errorMsg = err.Error()
			break
		}
		if s.smart != nil {
			if err := s.smart.Collect(ctx, disks); err != nil {
				errorMsg = err.Error()
			} else {
				success = true
				s.logger.Info("executed remote SMART collection command", "cmd_id", cmd.ID)
			}
		} else {
			errorMsg = "SMART collector not available"
		}

	case "collect_nvme":
		disks, err := s.store.ListDisks(ctx)
		if err != nil {
			errorMsg = err.Error()
			break
		}
		if s.nvme != nil {
			if err := s.nvme.Collect(ctx, disks); err != nil {
				errorMsg = err.Error()
			} else {
				success = true
				s.logger.Info("executed remote NVMe collection command", "cmd_id", cmd.ID)
			}
		} else {
			errorMsg = "NVMe collector not available"
		}

	case "collect_zfs":
		if s.zfs != nil {
			if err := s.zfs.Collect(ctx); err != nil {
				errorMsg = err.Error()
			} else {
				success = true
				s.logger.Info("executed remote ZFS collection command", "cmd_id", cmd.ID)
			}
		} else {
			errorMsg = "ZFS collector not available"
		}

	default:
		errorMsg = fmt.Sprintf("unknown command type: %s", cmd.Type)
	}

	// Acknowledge command
	if s.uplink != nil {
		if err := s.uplink.AcknowledgeCommand(ctx, cmd.ID, success, errorMsg); err != nil {
			s.logger.Warn("failed to acknowledge command", "cmd_id", cmd.ID, "error", err)
		}
	}
}
