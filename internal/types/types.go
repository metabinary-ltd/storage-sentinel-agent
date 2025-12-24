package types

type Disk struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"` // hdd | sata_ssd | nvme
	Model     string `json:"model,omitempty"`
	Serial    string `json:"serial,omitempty"`
	Firmware  string `json:"firmware,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

type Pool struct {
	Name  string `json:"name"`
	State string `json:"state,omitempty"`
}

type SmartSnapshot struct {
	DiskID             string  `json:"disk_id"`
	HealthStatus       string  `json:"health_status"`
	Reallocated        int64   `json:"reallocated"`
	Pending            int64   `json:"pending"`
	OfflineUncorrect   int64   `json:"offline_uncorrectable"`
	CRCErrors          int64   `json:"crc_errors"`
	TemperatureC       float64 `json:"temperature_c"`
	PowerOnHours       int64   `json:"power_on_hours"`
	TimestampUnixMilli int64   `json:"timestamp"`
}

type NvmeSnapshot struct {
	DiskID             string  `json:"disk_id"`
	PercentUsed        float64 `json:"percent_used"`
	MediaErrors        int64   `json:"media_errors"`
	ErrorLogEntries    int64   `json:"error_log_entries"`
	PowerOnHours       int64   `json:"power_on_hours"`
	UnsafeShutdowns    int64   `json:"unsafe_shutdowns"`
	TemperatureC       float64 `json:"temperature_c"`
	DataWrittenBytes   int64   `json:"data_written_bytes"`
	DataReadBytes      int64   `json:"data_read_bytes"`
	TimestampUnixMilli int64   `json:"timestamp"`
}

type PoolStatus struct {
	PoolName          string `json:"pool_name"`
	State             string `json:"state"`
	LastScrubTimeUnix int64  `json:"last_scrub_time"`
	LastScrubErrors   int64  `json:"last_scrub_errors"`
}

type DiskHealth struct {
	ID           string   `json:"id"`
	Name         string   `json:"name,omitempty"`
	Type         string   `json:"type,omitempty"`
	Status       string   `json:"status,omitempty"`
	HealthScore  int      `json:"health_score,omitempty"`
	TemperatureC float64  `json:"temperature_c,omitempty"`
	Issues       []string `json:"issues,omitempty"`
}

type PoolHealth struct {
	Name        string   `json:"name"`
	State       string   `json:"state,omitempty"`
	Status      string   `json:"status,omitempty"`
	HealthScore int      `json:"health_score,omitempty"`
	Issues      []string `json:"issues,omitempty"`
}

type Alert struct {
	ID           int64  `json:"id,omitempty"`
	Timestamp    int64  `json:"timestamp"`
	Severity     string `json:"severity"`
	SourceType   string `json:"source_type"`
	SourceID     string `json:"source_id"`
	Subject      string `json:"subject"`
	Message      string `json:"message"`
	Acknowledged bool   `json:"acknowledged,omitempty"`
}

type HealthReport struct {
	Status string       `json:"status"`
	Disks  []DiskHealth `json:"disks"`
	Pools  []PoolHealth `json:"pools"`
	Alerts []Alert      `json:"alerts,omitempty"`
}
