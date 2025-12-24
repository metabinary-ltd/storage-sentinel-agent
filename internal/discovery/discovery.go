package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/metabinary-ltd/storagesentinel/internal/config"
	"github.com/metabinary-ltd/storagesentinel/internal/debug"
	"github.com/metabinary-ltd/storagesentinel/internal/storage"
)

type Service struct {
	store     *storage.Store
	logger    *slog.Logger
	cfg       config.StorageConfig
	zpoolPath string
}

func New(store *storage.Store, logger *slog.Logger) *Service {
	return &Service{
		store:     store,
		logger:    logger,
		cfg:       config.StorageConfig{}, // Default empty config
		zpoolPath: "zpool",
	}
}

func NewWithConfig(store *storage.Store, cfg config.StorageConfig, zpoolPath string, logger *slog.Logger) *Service {
	return &Service{
		store:     store,
		logger:    logger,
		cfg:       cfg,
		zpoolPath: zpoolPath,
	}
}

// RunOnce performs a single discovery pass.
func (s *Service) RunOnce(ctx context.Context) error {
	disks, err := scanSysBlock()
	if err != nil {
		return err
	}

	// Apply device filtering
	disks = s.filterDevices(disks)

	for _, d := range disks {
		if err := s.store.UpsertDisk(ctx, d); err != nil {
			s.logger.Warn("failed to upsert disk", "disk", d.ID, "error", err)
		}
	}

	// Discover ZFS pools and their device mappings if enabled
	if s.cfg.ZFSEnable {
		if err := s.discoverZFS(ctx); err != nil {
			s.logger.Warn("zfs discovery failed", "error", err)
		}
	}

	return nil
}

func scanSysBlock() ([]storage.Disk, error) {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil, err
	}

	var disks []storage.Disk
	for _, e := range entries {
		name := e.Name()
		// basic filter: skip loop/ram/dm mapper devices
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "dm-") {
			continue
		}

		rotationalPath := filepath.Join("/sys/block", name, "queue/rotational")
		rotational, _ := os.ReadFile(rotationalPath)
		devType := classifyDevice(name, string(rotational))

		model := readTrim(filepath.Join("/sys/block", name, "device/model"))
		serial := readTrim(filepath.Join("/sys/block", name, "device/serial"))
		firmware := readTrim(filepath.Join("/sys/block", name, "device/rev"))
		sizeBytes := readSizeBytes(filepath.Join("/sys/block", name, "size"))
		idPath := byIDPath(name)
		disks = append(disks, storage.Disk{
			ID:        idPath,
			Name:      "/dev/" + name,
			Type:      devType,
			Model:     model,
			Serial:    serial,
			Firmware:   firmware,
			SizeBytes: sizeBytes,
		})
	}
	return disks, nil
}

func byIDPath(name string) string {
	byIDDir := "/dev/disk/by-id"
	entries, err := os.ReadDir(byIDDir)
	if err != nil {
		return "/dev/" + name
	}
	for _, e := range entries {
		full := filepath.Join(byIDDir, e.Name())
		target, err := os.Readlink(full)
		if err == nil && strings.HasSuffix(target, "/"+name) {
			return full
		}
	}
	return "/dev/" + name
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readSizeBytes(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	blocks, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	// size file is in 512-byte sectors
	return blocks * 512
}

func classifyDevice(name string, rotationalVal string) string {
	rotational := strings.TrimSpace(rotationalVal)
	if strings.HasPrefix(name, "nvme") {
		return "nvme"
	}
	if rotational == "1" {
		return "hdd"
	}
	return "sata_ssd"
}

func (s *Service) filterDevices(disks []storage.Disk) []storage.Disk {
	var filtered []storage.Disk

	for _, disk := range disks {
		// Check exclude patterns
		excluded := false
		for _, pattern := range s.cfg.ExcludeDevices {
			if matched, _ := filepath.Match(pattern, disk.ID); matched {
				excluded = true
				break
			}
			if matched, _ := filepath.Match(pattern, disk.Name); matched {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}

		// Check include patterns (if any are specified)
		if len(s.cfg.IncludeDevices) > 0 {
			included := false
			for _, pattern := range s.cfg.IncludeDevices {
				if matched, _ := filepath.Match(pattern, disk.ID); matched {
					included = true
					break
				}
				if matched, _ := filepath.Match(pattern, disk.Name); matched {
					included = true
					break
				}
			}
			if !included {
				continue
			}
		}

		filtered = append(filtered, disk)
	}

	return filtered
}

func (s *Service) discoverZFS(ctx context.Context) error {
	// #region agent log
	debug.Log("internal/discovery/discovery.go:191", "discoverZFS called", map[string]interface{}{
		"zpoolPath":  s.zpoolPath,
		"zfsEnabled": s.cfg.ZFSEnable,
	})
	// #endregion
	// Get list of pools
	cmd := exec.CommandContext(ctx, s.zpoolPath, "list", "-H", "-o", "name")
	out, err := cmd.Output()
	// #region agent log
	debug.Log("internal/discovery/discovery.go:197", "zpool list result", map[string]interface{}{
		"output": strings.TrimSpace(string(out)),
		"error":  fmt.Sprintf("%v", err),
	})
	// #endregion
	if err != nil {
		return err
	}

	poolNames := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) > 0 {
			poolNames = append(poolNames, parts[0])
		}
	}
	// #region agent log
	debug.Log("internal/discovery/discovery.go:212", "Parsed pool names from discovery", map[string]interface{}{
		"count": len(poolNames),
		"names": poolNames,
	})
	// #endregion

	// For each pool, get device mappings
	for _, poolName := range poolNames {
		if err := s.mapPoolDevices(ctx, poolName); err != nil {
			s.logger.Warn("failed to map pool devices", "pool", poolName, "error", err)
		}
	}

	return nil
}

func (s *Service) mapPoolDevices(ctx context.Context, poolName string) error {
	// Get zpool status output
	cmd := exec.CommandContext(ctx, s.zpoolPath, "status", poolName)
	out, err := cmd.Output()
	if err != nil {
		return err
	}

	statusOutput := string(out)
	deviceIDs := extractDevicesFromStatus(statusOutput)

	// Determine vdev type (simplified - could be enhanced)
	vdevType := "data" // Default
	if strings.Contains(strings.ToLower(statusOutput), "cache") {
		vdevType = "cache"
	} else if strings.Contains(strings.ToLower(statusOutput), "log") {
		vdevType = "log"
	} else if strings.Contains(strings.ToLower(statusOutput), "spare") {
		vdevType = "spare"
	}

	if len(deviceIDs) > 0 {
		if err := s.store.UpsertPoolDevices(ctx, poolName, deviceIDs, vdevType); err != nil {
			return err
		}
		s.logger.Debug("mapped pool devices", "pool", poolName, "devices", len(deviceIDs))
	}

	return nil
}

func extractDevicesFromStatus(statusOutput string) []string {
	var deviceIDs []string
	seen := make(map[string]bool)

	// Pattern 1: Look for device paths like /dev/sdX, /dev/nvmeXnY, /dev/disk/by-id/...
	devicePattern := regexp.MustCompile(`(/dev/(?:sd[a-z]+|nvme\d+n\d+|disk/by-id/[^\s]+))`)
	matches := devicePattern.FindAllString(statusOutput, -1)
	for _, match := range matches {
		if !seen[match] {
			deviceIDs = append(deviceIDs, match)
			seen[match] = true
		}
	}

	// Pattern 2: Look for device names without /dev/ prefix (sdX, nvmeXnY)
	shortPattern := regexp.MustCompile(`\b(sd[a-z]+|nvme\d+n\d+)\b`)
	shortMatches := shortPattern.FindAllString(statusOutput, -1)
	for _, match := range shortMatches {
		fullPath := "/dev/" + match
		if !seen[fullPath] {
			deviceIDs = append(deviceIDs, fullPath)
			seen[fullPath] = true
		}
	}

	// Convert device paths to by-id paths if possible
	for i, deviceID := range deviceIDs {
		if byID := resolveByID(deviceID); byID != deviceID {
			deviceIDs[i] = byID
		}
	}

	return deviceIDs
}

func resolveByID(devicePath string) string {
	// If already a by-id path, return as-is
	if strings.Contains(devicePath, "/disk/by-id/") {
		return devicePath
	}

	// Extract device name (e.g., "sda" from "/dev/sda")
	deviceName := strings.TrimPrefix(devicePath, "/dev/")

	// Look up in /dev/disk/by-id
	byIDDir := "/dev/disk/by-id"
	entries, err := os.ReadDir(byIDDir)
	if err != nil {
		return devicePath
	}

	for _, e := range entries {
		full := filepath.Join(byIDDir, e.Name())
		target, err := os.Readlink(full)
		if err == nil && strings.HasSuffix(target, "/"+deviceName) {
			return full
		}
	}

	return devicePath
}
