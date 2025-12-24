package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultConfigPath = "/etc/storagesentinel/config.yml"
)

type StorageConfig struct {
	IncludeDevices []string `yaml:"include_devices"`
	ExcludeDevices []string `yaml:"exclude_devices"`
	ZFSEnable      bool     `yaml:"zfs_enable"`
}

type SchedulingConfig struct {
	SmartCollectInterval time.Duration `yaml:"smart_collect_interval"`
	ZFSStatusInterval    time.Duration `yaml:"zfs_status_interval"`
	SmartShortInterval   time.Duration `yaml:"smart_short_interval"`
	SmartLongInterval    time.Duration `yaml:"smart_long_interval"`
	ZFSScrubInterval     time.Duration `yaml:"zfs_scrub_interval"`
}

type TemperatureThresholds struct {
	HDDWarning   float64 `yaml:"hdd_warning"`   // Warning threshold for HDDs (default: 55°C)
	HDDCritical  float64 `yaml:"hdd_critical"`  // Critical threshold for HDDs (default: 70°C)
	NvmeWarning  float64 `yaml:"nvme_warning"`  // Warning threshold for NVMe (default: 70°C)
	NvmeCritical float64 `yaml:"nvme_critical"` // Critical threshold for NVMe (default: 85°C)
}

type AlertsConfig struct {
	MinSeverity          string                 `yaml:"min_severity"`
	DebounceWindow       time.Duration          `yaml:"debounce_window"`
	TemperatureThresholds TemperatureThresholds `yaml:"temperature_thresholds,omitempty"`
}

type EmailConfig struct {
	Enabled    bool     `yaml:"enabled"`
	SMTPServer string   `yaml:"smtp_server"`
	SMTPPort   int      `yaml:"smtp_port"`
	Username   string   `yaml:"username"`
	Password   string   `yaml:"password"`
	From       string   `yaml:"from"`
	To         []string `yaml:"to"`
}

type TelegramConfig struct {
	Enabled  bool   `yaml:"enabled"`
	BotToken string `yaml:"bot_token"`
	ChatID   string `yaml:"chat_id"`
}

type WebhookConfig struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
}

type NotificationsConfig struct {
	Email    EmailConfig     `yaml:"email"`
	Telegram TelegramConfig  `yaml:"telegram"`
	Webhooks []WebhookConfig `yaml:"webhooks"`
}

type CloudConfig struct {
	Enabled            bool          `yaml:"enabled"`
	Endpoint           string        `yaml:"endpoint"`
	APIToken           string        `yaml:"api_token"`
	HostID             string        `yaml:"host_id,omitempty"` // Auto-generated on registration
	UploadInterval     time.Duration `yaml:"upload_interval"`
	CommandPollInterval time.Duration `yaml:"command_poll_interval"`
	Hostname           string        `yaml:"hostname,omitempty"` // Override hostname
}

type APIConfig struct {
	BindAddress string `yaml:"bind_address"`
	Port        int    `yaml:"port"`
	AuthToken   string `yaml:"auth_token"`
}

type LoggingConfig struct {
	Level      string `yaml:"level"`
	DebugLog   string `yaml:"debug_log,omitempty"`   // Path to debug log file (empty = disabled)
	DebugEnable bool  `yaml:"debug_enable,omitempty"` // Enable debug logging
}

type PathsConfig struct {
	DBPath  string `yaml:"db_path"`
	LogPath string `yaml:"log_path"`
}

type ToolsConfig struct {
	Smartctl string `yaml:"smartctl"`
	Nvme     string `yaml:"nvme"`
	Zpool    string `yaml:"zpool"`
	Zfs      string `yaml:"zfs"`
}

type Config struct {
	Storage       StorageConfig       `yaml:"storage"`
	Scheduling    SchedulingConfig    `yaml:"scheduling"`
	Alerts        AlertsConfig        `yaml:"alerts"`
	Notifications NotificationsConfig `yaml:"notifications"`
	Cloud         CloudConfig         `yaml:"cloud"`
	API           APIConfig           `yaml:"api"`
	Logging       LoggingConfig       `yaml:"logging"`
	Paths         PathsConfig         `yaml:"paths"`
	Tools         ToolsConfig         `yaml:"tools"`
}

func defaultConfig() Config {
	return Config{
		Storage: StorageConfig{
			IncludeDevices: []string{},
			ExcludeDevices: []string{},
			ZFSEnable:      true,
		},
		Scheduling: SchedulingConfig{
			SmartCollectInterval: 6 * time.Hour,
			ZFSStatusInterval:    15 * time.Minute,
			SmartShortInterval:   168 * time.Hour,
			SmartLongInterval:    720 * time.Hour,
			ZFSScrubInterval:     720 * time.Hour,
		},
		Alerts: AlertsConfig{
			MinSeverity:    "warning",
			DebounceWindow: 6 * time.Hour,
			TemperatureThresholds: TemperatureThresholds{
				HDDWarning:   55.0, // Default: 55°C warning for HDDs
				HDDCritical:  70.0, // Default: 70°C critical for HDDs
				NvmeWarning:  70.0, // Default: 70°C warning for NVMe
				NvmeCritical: 85.0, // Default: 85°C critical for NVMe
			},
		},
		Notifications: NotificationsConfig{
			Email: EmailConfig{
				Enabled:    false,
				SMTPServer: "",
				SMTPPort:   587,
				Username:   "",
				Password:   "",
				From:       "",
				To:         []string{},
			},
			Telegram: TelegramConfig{
				Enabled:  false,
				BotToken: "",
				ChatID:   "",
			},
			Webhooks: []WebhookConfig{},
		},
		Cloud: CloudConfig{
			Enabled:            false,
			Endpoint:           "https://api.storagesentinel.io",
			APIToken:           "",
			HostID:             "",
			UploadInterval:     15 * time.Minute,
			CommandPollInterval: 5 * time.Minute,
			Hostname:           "",
		},
		API: APIConfig{
			BindAddress: "127.0.0.1",
			Port:        8200,
		AuthToken:   "",
		},
		Logging: LoggingConfig{
			Level:       "info",
			DebugLog:    "/var/log/storagesentinel-debug.log",
			DebugEnable: false, // Disabled by default, enable for troubleshooting
		},
		Paths: PathsConfig{
			DBPath:  "/var/lib/storagesentinel/state.db",
			LogPath: "/var/log/storagesentinel.log",
		},
	Tools: ToolsConfig{
		Smartctl: "smartctl",
		Nvme:     "nvme",
		Zpool:    "zpool",
		Zfs:      "zfs",
	},
	}
}

func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultConfigPath
	}

	cfg := defaultConfig()

	if fileExists(path) {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		if err := yaml.Unmarshal(content, &cfg); err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
	}

	applyEnvOverrides(&cfg)

	if err := validate(cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyEnvOverrides(cfg *Config) {
	if v, ok := os.LookupEnv("STORAGESENTINEL_API_BIND"); ok && v != "" {
		cfg.API.BindAddress = v
	}
	if v, ok := os.LookupEnv("STORAGESENTINEL_API_PORT"); ok && v != "" {
		if port, err := parseInt(v); err == nil {
			cfg.API.Port = port
		}
	}
	if v, ok := os.LookupEnv("STORAGESENTINEL_LOG_LEVEL"); ok && v != "" {
		cfg.Logging.Level = strings.ToLower(v)
	}
	if v, ok := os.LookupEnv("STORAGESENTINEL_DB_PATH"); ok && v != "" {
		cfg.Paths.DBPath = v
	}
	if v, ok := os.LookupEnv("STORAGESENTINEL_API_TOKEN"); ok && v != "" {
		cfg.API.AuthToken = v
	}
}

func validate(cfg Config) error {
	if cfg.API.Port <= 0 || cfg.API.Port > 65535 {
		return errors.New("api.port must be between 1 and 65535")
	}
	if cfg.API.BindAddress == "" {
		return errors.New("api.bind_address must be set")
	}
	return nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func parseInt(v string) (int, error) {
	var n int
	_, err := fmt.Sscanf(v, "%d", &n)
	return n, err
}
