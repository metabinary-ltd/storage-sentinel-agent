package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/metabinary-ltd/storagesentinel/internal/debug"
	_ "modernc.org/sqlite"
)

type Store struct {
	db     *sql.DB
	logger *slog.Logger
}

type Alert struct {
	ID           int64
	Severity     string
	SourceType   string
	SourceID     string
	Subject      string
	Message      string
	Timestamp    int64
	Acknowledged bool
}

type PoolStatus struct {
	Name           string
	State          string
	LastScrubTime  sql.NullInt64
	LastScrubError sql.NullInt64
}

type SmartSnapshot struct {
	DiskID           string
	HealthStatus     string
	Reallocated      int64
	Pending          int64
	OfflineUncorrect int64
	CRCErrors        int64
	TemperatureC     float64
	PowerOnHours     int64
	SpinRetryCount   int64
	LoadCycleCount   int64
	RawJSON          string
	Timestamp        int64
}

type NvmeSnapshot struct {
	DiskID               string
	PercentUsed          float64
	MediaErrors          int64
	ErrorLogEntries      int64
	PowerOnHours         int64
	UnsafeShutdowns      int64
	TemperatureC         float64
	DataWrittenBytes     int64
	DataReadBytes        int64
	CriticalWarningFlags string
	RawOutput            string
	Timestamp            int64
}

func Open(dbPath string, logger *slog.Logger) (*Store, error) {
	if err := os.MkdirAll(dirOf(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL;`); err != nil {
		return nil, fmt.Errorf("set WAL: %w", err)
	}

	s := &Store{db: db, logger: logger}
	if err := s.initSchema(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) initSchema() error {
	schema := []string{
		`CREATE TABLE IF NOT EXISTS meta (
			key TEXT PRIMARY KEY,
			value TEXT
		);`,
		`CREATE TABLE IF NOT EXISTS smart_test_schedule (
			disk_id TEXT,
			test_type TEXT,
			last_run_time TIMESTAMP,
			PRIMARY KEY (disk_id, test_type),
			FOREIGN KEY (disk_id) REFERENCES disks(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS disks (
			id TEXT PRIMARY KEY,
			name TEXT,
			type TEXT,
			model TEXT,
			serial TEXT,
			firmware TEXT,
			size_bytes INTEGER,
			first_seen TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			last_seen TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS smart_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			disk_id TEXT,
			timestamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			health_status TEXT,
			reallocated INTEGER,
			pending INTEGER,
			offline_uncorrectable INTEGER,
			crc_errors INTEGER,
			temperature_c REAL,
			power_on_hours INTEGER,
			spin_retry_count INTEGER,
			load_cycle_count INTEGER,
			raw_json TEXT,
			FOREIGN KEY (disk_id) REFERENCES disks(id)
		);`,
		`CREATE TABLE IF NOT EXISTS nvme_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			disk_id TEXT,
			timestamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			percent_used REAL,
			media_errors INTEGER,
			error_log_entries INTEGER,
			power_on_hours INTEGER,
			unsafe_shutdowns INTEGER,
			temperature_c REAL,
			data_written_bytes INTEGER,
			data_read_bytes INTEGER,
			critical_warning_flags TEXT,
			raw_output TEXT,
			FOREIGN KEY (disk_id) REFERENCES disks(id)
		);`,
		`CREATE TABLE IF NOT EXISTS zfs_pools (
			name TEXT PRIMARY KEY,
			state TEXT,
			last_scrub_time TIMESTAMP,
			last_scrub_errors INTEGER
		);`,
		`CREATE TABLE IF NOT EXISTS zfs_scrub_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			pool_name TEXT,
			start_time TIMESTAMP,
			end_time TIMESTAMP,
			errors INTEGER,
			bytes_processed INTEGER,
			notes TEXT,
			FOREIGN KEY (pool_name) REFERENCES zfs_pools(name)
		);`,
		`CREATE TABLE IF NOT EXISTS zfs_pool_devices (
			pool_name TEXT,
			disk_id TEXT,
			vdev_type TEXT,
			PRIMARY KEY (pool_name, disk_id),
			FOREIGN KEY (pool_name) REFERENCES zfs_pools(name) ON DELETE CASCADE,
			FOREIGN KEY (disk_id) REFERENCES disks(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS alerts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			severity TEXT,
			source_type TEXT,
			source_id TEXT,
			subject TEXT,
			message TEXT,
			acknowledged INTEGER DEFAULT 0
		);`,
		`CREATE TABLE IF NOT EXISTS notification_queue (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			alert_id INTEGER,
			channel TEXT,
			status TEXT,
			attempts INTEGER DEFAULT 0,
			last_attempt TIMESTAMP,
			next_retry TIMESTAMP,
			error_message TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			sent_at TIMESTAMP,
			FOREIGN KEY (alert_id) REFERENCES alerts(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS cloud_schedules (
			id TEXT PRIMARY KEY,
			task_type TEXT NOT NULL,
			schedule_type TEXT NOT NULL,
			schedule_value TEXT NOT NULL,
			enabled INTEGER DEFAULT 1,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);`,
	}

	for _, stmt := range schema {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("apply schema: %w", err)
		}
	}

	// Migrate existing databases to add new columns
	s.migrateSchema()

	_, _ = s.db.Exec(`INSERT OR IGNORE INTO meta(key,value) VALUES ('schema_version','1')`)
	return nil
}

func (s *Store) migrateSchema() {
	// Add new columns to smart_snapshots if they don't exist
	// SQLite doesn't support IF NOT EXISTS for ALTER TABLE, so we ignore errors
	_ = s.addColumnIfNotExists("smart_snapshots", "spin_retry_count", "INTEGER")
	_ = s.addColumnIfNotExists("smart_snapshots", "load_cycle_count", "INTEGER")
	_ = s.addColumnIfNotExists("disks", "firmware", "TEXT")
	_ = s.addColumnIfNotExists("nvme_snapshots", "raw_output", "TEXT")
}

func (s *Store) addColumnIfNotExists(table, column, colType string) error {
	// Check if column exists by querying table info
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var typeName string
		var notnull int
		var dfltValue sql.NullString
		var pk int

		if err := rows.Scan(&cid, &name, &typeName, &notnull, &dfltValue, &pk); err != nil {
			continue
		}
		if name == column {
			// Column already exists
			return nil
		}
	}

	// Column doesn't exist, add it
	_, err = s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, colType))
	return err
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}

// Disk represents a discovered disk row.
type Disk struct {
	ID        string
	Name      string
	Type      string
	Model     string
	Serial    string
	Firmware  string
	SizeBytes int64
	FirstSeen string
	LastSeen  string
}

func (s *Store) UpsertDisk(ctx context.Context, d Disk) error {
	if d.ID == "" {
		return errors.New("disk id required")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO disks (id, name, type, model, serial, firmware, size_bytes)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name,
			type=excluded.type,
			model=excluded.model,
			serial=excluded.serial,
			firmware=excluded.firmware,
			size_bytes=excluded.size_bytes,
			last_seen=CURRENT_TIMESTAMP
	`, d.ID, d.Name, d.Type, d.Model, d.Serial, d.Firmware, d.SizeBytes)
	return err
}

func (s *Store) ListDisks(ctx context.Context) ([]Disk, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, type, model, serial, firmware, size_bytes FROM disks ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []Disk
	for rows.Next() {
		var d Disk
		var firmware sql.NullString
		if err := rows.Scan(&d.ID, &d.Name, &d.Type, &d.Model, &d.Serial, &firmware, &d.SizeBytes); err != nil {
			return nil, err
		}
		d.Firmware = firmware.String
		res = append(res, d)
	}
	return res, rows.Err()
}

func (s *Store) GetDisk(ctx context.Context, id string) (*Disk, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, type, model, serial, firmware, size_bytes FROM disks WHERE id=?`, id)
	var d Disk
	var firmware sql.NullString
	if err := row.Scan(&d.ID, &d.Name, &d.Type, &d.Model, &d.Serial, &firmware, &d.SizeBytes); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	d.Firmware = firmware.String
	return &d, nil
}

// GetDiskPoolMembership returns pool membership information for a disk
func (s *Store) GetDiskPoolMembership(ctx context.Context, diskID string) ([]struct {
	PoolName string
	VdevType string
}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT pool_name, vdev_type FROM zfs_pool_devices WHERE disk_id=?`, diskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var memberships []struct {
		PoolName string
		VdevType string
	}
	for rows.Next() {
		var m struct {
			PoolName string
			VdevType string
		}
		if err := rows.Scan(&m.PoolName, &m.VdevType); err != nil {
			return nil, err
		}
		memberships = append(memberships, m)
	}
	return memberships, rows.Err()
}

func (s *Store) UpsertPool(ctx context.Context, name, state string, lastScrubTime int64, lastScrubErrors int64) error {
	if name == "" {
		return errors.New("pool name required")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO zfs_pools (name, state, last_scrub_time, last_scrub_errors)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			state=excluded.state,
			last_scrub_time=excluded.last_scrub_time,
			last_scrub_errors=excluded.last_scrub_errors
	`, name, state, nullTime(lastScrubTime), nullInt(lastScrubErrors))
	return err
}

func (s *Store) ListPools(ctx context.Context) ([]PoolStatus, error) {
	// #region agent log
	debug.Log("internal/storage/sqlite.go:331", "ListPools called", map[string]interface{}{})
	// #endregion
	rows, err := s.db.QueryContext(ctx, `SELECT name, state, last_scrub_time, last_scrub_errors FROM zfs_pools ORDER BY name`)
	// #region agent log
	debug.Log("internal/storage/sqlite.go:335", "ListPools query executed", map[string]interface{}{
		"error": fmt.Sprintf("%v", err),
	})
	// #endregion
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []PoolStatus
	for rows.Next() {
		var p PoolStatus
		if err := rows.Scan(&p.Name, &p.State, &p.LastScrubTime, &p.LastScrubError); err != nil {
			return nil, err
		}
		res = append(res, p)
	}
	// #region agent log
	debug.Log("internal/storage/sqlite.go:346", "ListPools result", map[string]interface{}{
		"count": len(res),
	})
	// #endregion
	return res, rows.Err()
}

// UpsertPoolDevices updates the device mapping for a pool
func (s *Store) UpsertPoolDevices(ctx context.Context, poolName string, deviceIDs []string, vdevType string) error {
	// Delete existing mappings for this pool
	_, err := s.db.ExecContext(ctx, `DELETE FROM zfs_pool_devices WHERE pool_name=?`, poolName)
	if err != nil {
		return err
	}

	// Insert new mappings
	for _, diskID := range deviceIDs {
		if diskID == "" {
			continue
		}
		_, err := s.db.ExecContext(ctx, `
			INSERT INTO zfs_pool_devices (pool_name, disk_id, vdev_type)
			VALUES (?, ?, ?)
		`, poolName, diskID, vdevType)
		if err != nil {
			// Log but continue - some devices might not be in disks table yet
			continue
		}
	}
	return nil
}

// GetPoolDevices returns the list of device IDs for a pool
func (s *Store) GetPoolDevices(ctx context.Context, poolName string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT disk_id FROM zfs_pool_devices WHERE pool_name=?`, poolName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var deviceIDs []string
	for rows.Next() {
		var diskID string
		if err := rows.Scan(&diskID); err != nil {
			return nil, err
		}
		deviceIDs = append(deviceIDs, diskID)
	}
	return deviceIDs, rows.Err()
}

func (s *Store) AddSmartSnapshot(ctx context.Context, snap SmartSnapshot) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO smart_snapshots (
			disk_id, timestamp, health_status, reallocated, pending,
			offline_uncorrectable, crc_errors, temperature_c, power_on_hours,
			spin_retry_count, load_cycle_count, raw_json)
		VALUES (?, datetime(?,'unixepoch'), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, snap.DiskID, snap.Timestamp, snap.HealthStatus, snap.Reallocated, snap.Pending,
		snap.OfflineUncorrect, snap.CRCErrors, snap.TemperatureC, snap.PowerOnHours,
		snap.SpinRetryCount, snap.LoadCycleCount, snap.RawJSON)
	return err
}

func (s *Store) AddNvmeSnapshot(ctx context.Context, snap NvmeSnapshot) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO nvme_snapshots (
			disk_id, timestamp, percent_used, media_errors, error_log_entries,
			power_on_hours, unsafe_shutdowns, temperature_c, data_written_bytes, data_read_bytes, critical_warning_flags, raw_output)
		VALUES (?, datetime(?,'unixepoch'), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, snap.DiskID, snap.Timestamp, snap.PercentUsed, snap.MediaErrors, snap.ErrorLogEntries,
		snap.PowerOnHours, snap.UnsafeShutdowns, snap.TemperatureC, snap.DataWrittenBytes, snap.DataReadBytes,
		snap.CriticalWarningFlags, snap.RawOutput)
	return err
}

func (s *Store) LatestSmart(ctx context.Context, diskID string) (*SmartSnapshot, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT disk_id, strftime('%s', timestamp), health_status, reallocated, pending,
			offline_uncorrectable, crc_errors, temperature_c, power_on_hours,
			spin_retry_count, load_cycle_count, raw_json
		FROM smart_snapshots
		WHERE disk_id=?
		ORDER BY timestamp DESC LIMIT 1
	`, diskID)
	var snap SmartSnapshot
	if err := row.Scan(&snap.DiskID, &snap.Timestamp, &snap.HealthStatus, &snap.Reallocated, &snap.Pending,
		&snap.OfflineUncorrect, &snap.CRCErrors, &snap.TemperatureC, &snap.PowerOnHours,
		&snap.SpinRetryCount, &snap.LoadCycleCount, &snap.RawJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &snap, nil
}

func (s *Store) LatestNvme(ctx context.Context, diskID string) (*NvmeSnapshot, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT disk_id, strftime('%s', timestamp), percent_used, media_errors, error_log_entries,
			power_on_hours, unsafe_shutdowns, temperature_c, data_written_bytes, data_read_bytes, critical_warning_flags, COALESCE(raw_output, '')
		FROM nvme_snapshots
		WHERE disk_id=?
		ORDER BY timestamp DESC LIMIT 1
	`, diskID)
	var snap NvmeSnapshot
	var rawOutput sql.NullString
	if err := row.Scan(&snap.DiskID, &snap.Timestamp, &snap.PercentUsed, &snap.MediaErrors, &snap.ErrorLogEntries,
		&snap.PowerOnHours, &snap.UnsafeShutdowns, &snap.TemperatureC, &snap.DataWrittenBytes, &snap.DataReadBytes,
		&snap.CriticalWarningFlags, &rawOutput); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	snap.RawOutput = rawOutput.String
	return &snap, nil
}

func (s *Store) SmartHistory(ctx context.Context, diskID string, limit int) ([]SmartSnapshot, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT disk_id, strftime('%s', timestamp), health_status, reallocated, pending,
			offline_uncorrectable, crc_errors, temperature_c, power_on_hours,
			spin_retry_count, load_cycle_count, raw_json
		FROM smart_snapshots
		WHERE disk_id=?
		ORDER BY timestamp DESC
		LIMIT ?
	`, diskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []SmartSnapshot
	for rows.Next() {
		var snap SmartSnapshot
		if err := rows.Scan(&snap.DiskID, &snap.Timestamp, &snap.HealthStatus, &snap.Reallocated, &snap.Pending,
			&snap.OfflineUncorrect, &snap.CRCErrors, &snap.TemperatureC, &snap.PowerOnHours,
			&snap.SpinRetryCount, &snap.LoadCycleCount, &snap.RawJSON); err != nil {
			return nil, err
		}
		res = append(res, snap)
	}
	return res, rows.Err()
}

func (s *Store) NvmeHistory(ctx context.Context, diskID string, limit int) ([]NvmeSnapshot, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT disk_id, strftime('%s', timestamp), percent_used, media_errors, error_log_entries,
			power_on_hours, unsafe_shutdowns, temperature_c, data_written_bytes, data_read_bytes, critical_warning_flags, COALESCE(raw_output, '')
		FROM nvme_snapshots
		WHERE disk_id=?
		ORDER BY timestamp DESC
		LIMIT ?
	`, diskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []NvmeSnapshot
	for rows.Next() {
		var snap NvmeSnapshot
		var rawOutput sql.NullString
		if err := rows.Scan(&snap.DiskID, &snap.Timestamp, &snap.PercentUsed, &snap.MediaErrors, &snap.ErrorLogEntries,
			&snap.PowerOnHours, &snap.UnsafeShutdowns, &snap.TemperatureC, &snap.DataWrittenBytes, &snap.DataReadBytes,
			&snap.CriticalWarningFlags, &rawOutput); err != nil {
			return nil, err
		}
		snap.RawOutput = rawOutput.String
		res = append(res, snap)
	}
	return res, rows.Err()
}

func (s *Store) AddAlert(ctx context.Context, a Alert) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO alerts (timestamp, severity, source_type, source_id, subject, message)
		VALUES (datetime(?,'unixepoch'), ?, ?, ?, ?, ?)
	`, a.Timestamp, a.Severity, a.SourceType, a.SourceID, a.Subject, a.Message)
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	return id, err
}

func (s *Store) RecentAlerts(ctx context.Context, limit int) ([]Alert, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, strftime('%s', timestamp), severity, source_type, source_id, subject, message, acknowledged
		FROM alerts
		ORDER BY timestamp DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []Alert
	for rows.Next() {
		var a Alert
		var ack int
		if err := rows.Scan(&a.ID, &a.Timestamp, &a.Severity, &a.SourceType, &a.SourceID, &a.Subject, &a.Message, &ack); err != nil {
			return nil, err
		}
		a.Acknowledged = ack != 0
		res = append(res, a)
	}
	return res, rows.Err()
}

// PruneOldSnapshots removes snapshots older than the given age in days.
func (s *Store) PruneOldSnapshots(ctx context.Context, days int) error {
	if days <= 0 {
		days = 90
	}
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM smart_snapshots WHERE timestamp < datetime('now', ?);
	`, fmt.Sprintf("-%d days", days))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		DELETE FROM nvme_snapshots WHERE timestamp < datetime('now', ?);
	`, fmt.Sprintf("-%d days", days))
	return err
}

// ScrubHistoryEntry represents a scrub history record
type ScrubHistoryEntry struct {
	PoolName       string
	StartTime      int64
	EndTime        int64
	Errors         int64
	BytesProcessed int64
	Notes          string
}

func (s *Store) AddScrubHistory(ctx context.Context, entry ScrubHistoryEntry) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO zfs_scrub_history (
			pool_name, start_time, end_time, errors, bytes_processed, notes)
		VALUES (?, datetime(?,'unixepoch'), datetime(?,'unixepoch'), ?, ?, ?)
	`, entry.PoolName, entry.StartTime, entry.EndTime, entry.Errors, entry.BytesProcessed, entry.Notes)
	return err
}

// GetScrubHistory returns scrub history for a pool
func (s *Store) GetScrubHistory(ctx context.Context, poolName string, limit int) ([]ScrubHistoryEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT pool_name, strftime('%s', start_time), strftime('%s', end_time),
			errors, bytes_processed, notes
		FROM zfs_scrub_history
		WHERE pool_name = ?
		ORDER BY start_time DESC
		LIMIT ?
	`, poolName, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []ScrubHistoryEntry
	for rows.Next() {
		var e ScrubHistoryEntry
		var startTime, endTime sql.NullInt64
		if err := rows.Scan(&e.PoolName, &startTime, &endTime, &e.Errors, &e.BytesProcessed, &e.Notes); err != nil {
			return nil, err
		}
		if startTime.Valid {
			e.StartTime = startTime.Int64
		}
		if endTime.Valid {
			e.EndTime = endTime.Int64
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func nullTime(ts int64) any {
	if ts <= 0 {
		return nil
	}
	return ts
}

func nullInt(v int64) any {
	return v
}

// GetLastSmartTestTime returns the last time a SMART test was run for a disk
func (s *Store) GetLastSmartTestTime(ctx context.Context, diskID, testType string) (int64, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT strftime('%s', last_run_time) FROM smart_test_schedule
		WHERE disk_id=? AND test_type=?
	`, diskID, testType)

	var lastRun sql.NullInt64
	if err := row.Scan(&lastRun); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil // Never run
		}
		return 0, err
	}
	if !lastRun.Valid {
		return 0, nil
	}
	return lastRun.Int64, nil
}

// RecordSmartTest records that a SMART test was started
func (s *Store) RecordSmartTest(ctx context.Context, diskID, testType string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO smart_test_schedule (disk_id, test_type, last_run_time)
		VALUES (?, ?, datetime('now'))
		ON CONFLICT(disk_id, test_type) DO UPDATE SET
			last_run_time=excluded.last_run_time
	`, diskID, testType)
	return err
}

// GetLastScrubTime returns the last scrub time for a pool (from zfs_pools table)
func (s *Store) GetLastScrubTime(ctx context.Context, poolName string) (int64, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT strftime('%s', last_scrub_time) FROM zfs_pools WHERE name=?
	`, poolName)

	var lastScrub sql.NullInt64
	if err := row.Scan(&lastScrub); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil // Pool not found or never scrubbed
		}
		return 0, err
	}
	if !lastScrub.Valid {
		return 0, nil
	}
	return lastScrub.Int64, nil
}

// NotificationQueueEntry represents a queued notification
type NotificationQueueEntry struct {
	ID           int64
	AlertID      int64
	Channel      string
	Status       string // pending, sent, failed
	Attempts     int
	LastAttempt  sql.NullInt64
	NextRetry    sql.NullInt64
	ErrorMessage sql.NullString
	CreatedAt    int64
	SentAt       sql.NullInt64
}

// EnqueueNotification adds a notification to the queue
func (s *Store) EnqueueNotification(ctx context.Context, alertID int64, channel string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO notification_queue (alert_id, channel, status, next_retry)
		VALUES (?, ?, 'pending', datetime('now'))
	`, alertID, channel)
	return err
}

// GetPendingNotifications returns notifications that need to be sent
func (s *Store) GetPendingNotifications(ctx context.Context, limit int) ([]NotificationQueueEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, alert_id, channel, status, attempts,
			strftime('%s', last_attempt), strftime('%s', next_retry),
			error_message, strftime('%s', created_at), strftime('%s', sent_at)
		FROM notification_queue
		WHERE status = 'pending' AND (next_retry IS NULL OR next_retry <= datetime('now'))
		ORDER BY created_at ASC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []NotificationQueueEntry
	for rows.Next() {
		var e NotificationQueueEntry
		if err := rows.Scan(&e.ID, &e.AlertID, &e.Channel, &e.Status, &e.Attempts,
			&e.LastAttempt, &e.NextRetry, &e.ErrorMessage, &e.CreatedAt, &e.SentAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// MarkNotificationSent marks a notification as successfully sent
func (s *Store) MarkNotificationSent(ctx context.Context, queueID int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE notification_queue
		SET status = 'sent', sent_at = datetime('now'), next_retry = NULL
		WHERE id = ?
	`, queueID)
	return err
}

// MarkNotificationFailed marks a notification as failed and schedules retry
func (s *Store) MarkNotificationFailed(ctx context.Context, queueID int64, errorMsg string, nextRetry time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE notification_queue
		SET status = 'pending', attempts = attempts + 1,
			last_attempt = datetime('now'), next_retry = datetime(?,'unixepoch'),
			error_message = ?
		WHERE id = ?
	`, nextRetry.Unix(), errorMsg, queueID)
	return err
}

// GetUnsentNotificationCount returns the count of unsent notifications
func (s *Store) GetUnsentNotificationCount(ctx context.Context) (int, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM notification_queue WHERE status = 'pending'
	`)
	var count int
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// GetAlert retrieves an alert by ID
func (s *Store) GetAlert(ctx context.Context, alertID int64) (*Alert, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, strftime('%s', timestamp), severity, source_type, source_id, subject, message, acknowledged
		FROM alerts WHERE id = ?
	`, alertID)

	var a Alert
	var ts int64
	var ack int
	if err := row.Scan(&a.ID, &ts, &a.Severity, &a.SourceType, &a.SourceID, &a.Subject, &a.Message, &ack); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	a.Timestamp = ts
	a.Acknowledged = ack != 0
	return &a, nil
}

// AcknowledgeAlert marks an alert as acknowledged
func (s *Store) AcknowledgeAlert(ctx context.Context, alertID int64) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE alerts
		SET acknowledged = 1
		WHERE id = ?
	`, alertID)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return errors.New("alert not found")
	}
	return nil
}

// CloudSchedule represents a schedule from the cloud
type CloudSchedule struct {
	ID           string
	TaskType     string
	ScheduleType string
	ScheduleValue string
	Enabled      bool
	UpdatedAt    int64
}

// StoreSchedules stores or updates cloud schedules
func (s *Store) StoreSchedules(ctx context.Context, schedules []CloudSchedule) error {
	for _, schedule := range schedules {
		enabled := 0
		if schedule.Enabled {
			enabled = 1
		}
		_, err := s.db.ExecContext(ctx, `
			INSERT INTO cloud_schedules (id, task_type, schedule_type, schedule_value, enabled, updated_at)
			VALUES (?, ?, ?, ?, ?, datetime('now'))
			ON CONFLICT(id) DO UPDATE SET
				task_type = excluded.task_type,
				schedule_type = excluded.schedule_type,
				schedule_value = excluded.schedule_value,
				enabled = excluded.enabled,
				updated_at = datetime('now')
		`, schedule.ID, schedule.TaskType, schedule.ScheduleType, schedule.ScheduleValue, enabled)
		if err != nil {
			return fmt.Errorf("store schedule %s: %w", schedule.ID, err)
		}
	}
	return nil
}

// ListSchedules returns all stored cloud schedules
func (s *Store) ListSchedules(ctx context.Context) ([]CloudSchedule, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, task_type, schedule_type, schedule_value, enabled, strftime('%s', updated_at)
		FROM cloud_schedules
		WHERE enabled = 1
		ORDER BY task_type, updated_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var schedules []CloudSchedule
	for rows.Next() {
		var sched CloudSchedule
		var enabled int
		if err := rows.Scan(&sched.ID, &sched.TaskType, &sched.ScheduleType, &sched.ScheduleValue, &enabled, &sched.UpdatedAt); err != nil {
			return nil, err
		}
		sched.Enabled = enabled != 0
		schedules = append(schedules, sched)
	}
	return schedules, rows.Err()
}

// GetScheduleForTask returns the schedule for a specific task type
func (s *Store) GetScheduleForTask(ctx context.Context, taskType string) (*CloudSchedule, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, task_type, schedule_type, schedule_value, enabled, strftime('%s', updated_at)
		FROM cloud_schedules
		WHERE task_type = ? AND enabled = 1
		ORDER BY updated_at DESC
		LIMIT 1
	`, taskType)

	var sched CloudSchedule
	var enabled int
	if err := row.Scan(&sched.ID, &sched.TaskType, &sched.ScheduleType, &sched.ScheduleValue, &enabled, &sched.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	sched.Enabled = enabled != 0
	return &sched, nil
}
