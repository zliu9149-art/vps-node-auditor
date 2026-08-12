// Package config loads and validates node-auditor's local JSON configuration.
// 中文：config 包负责加载并校验 node-auditor 的本地 JSON 配置。
package config

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	_ "time/tzdata" // Keep configured IANA timezones available in static binaries.
)

const defaultInterval = time.Minute

// Config contains settings shared by the collector and read-only CLI.
// 中文：Config 包含采集器和只读查询 CLI 共用的配置。
type Config struct {
	Database    DatabaseConfig     `json:"database"`
	Collector   CollectorConfig    `json:"collector"`
	Host        HostConfig         `json:"host"`
	Display     DisplayConfig      `json:"display"`
	Provider    ProviderConfig     `json:"provider"`
	Retention   RetentionConfig    `json:"retention"`
	Alerts      AlertConfig        `json:"alerts"`
	Credentials []CredentialConfig `json:"credentials"`
}

// DatabaseConfig locates the single local SQLite database.
// 中文：DatabaseConfig 指定唯一的本地 SQLite 数据库位置。
type DatabaseConfig struct {
	Path string `json:"path"`
}

// CollectorConfig selects the local statistics source and polling interval.
// 中文：CollectorConfig 选择本地统计数据源和轮询周期。
type CollectorConfig struct {
	Source         string         `json:"source"`
	Interval       string         `json:"interval"`
	MockFile       string         `json:"mock_file,omitempty"`
	ConfigLockPath string         `json:"config_lock_path"`
	V2RayAPI       V2RayAPIConfig `json:"v2ray_api,omitempty"`
}

// HostConfig controls the bounded Linux host evidence source.
// 中文：HostConfig 控制范围受限的 Linux 主机证据源。
type HostConfig struct {
	Disabled       bool   `json:"disabled"`
	Interface      string `json:"interface"`
	CommandTimeout string `json:"command_timeout"`
}

// V2RayAPIConfig configures the loopback-only sing-box V2Ray Stats client.
// 中文：V2RayAPIConfig 配置仅允许连接回环地址的 sing-box V2Ray Stats 客户端。
type V2RayAPIConfig struct {
	Address     string `json:"address"`
	Timeout     string `json:"timeout"`
	ProcessName string `json:"process_name"`
}

// DisplayConfig controls how UTC timestamps are rendered to the operator.
// 中文：DisplayConfig 控制 UTC 时间戳向操作人员显示时采用的时区。
type DisplayConfig struct {
	Timezone string `json:"timezone"`
}

// ProviderConfig describes the provider's quota without pretending to be the
// provider's authoritative billing system.
// 中文：ProviderConfig 描述服务商配额，但本地估算不冒充服务商权威计费结果。
type ProviderConfig struct {
	MonthlyQuotaBytes int64  `json:"monthly_quota_bytes"`
	CycleStartDay     int    `json:"cycle_start_day"`
	BillingTimezone   string `json:"billing_timezone"`
	TrafficDirection  string `json:"traffic_direction"`
}

// RetentionConfig defines local raw, event, rollup, and backup lifetimes.
// 中文：RetentionConfig 定义本地原始数据、事件、汇总和备份保留时间。
type RetentionConfig struct {
	MinuteDays  int `json:"minute_days"`
	EventDays   int `json:"event_days"`
	RollupDays  int `json:"rollup_days"`
	BackupCount int `json:"backup_count"`
}

// AlertConfig contains local journal/database-only alert thresholds.
// Zero user thresholds keep the optional rules disabled.
// 中文：AlertConfig 包含仅写入本地日志/数据库的告警阈值；用户阈值为零时，
// 对应可选规则保持关闭。
type AlertConfig struct {
	QuotaPercentages      []float64 `json:"quota_percentages"`
	TrafficDifferencePct  float64   `json:"traffic_difference_percent"`
	ConsecutiveFailures   int       `json:"consecutive_failures"`
	UserHourlyBytes       int64     `json:"user_hourly_bytes"`
	UserDailyBytes        int64     `json:"user_daily_bytes"`
	HistoricalSpikeFactor float64   `json:"historical_spike_factor"`
}

// CredentialConfig seeds initial credentials and backfills Stats names during
// upgrades. Once present, enabled state is owned by SQLite/node-provision so a
// collector restart cannot re-enable a revoked credential.
// 中文：CredentialConfig 用于初始化凭据并在升级时补齐 Stats 名称；凭据入库后，
// 启用状态由 SQLite/node-provision 管理，采集器重启不会重新启用已撤销凭据。
type CredentialConfig struct {
	UserName       string `json:"user_name"`
	CredentialName string `json:"credential_name"`
	UUID           string `json:"uuid"`
	StatsUser      string `json:"stats_user,omitempty"`
	Enabled        bool   `json:"enabled"`
}

// Load reads path, applies safe defaults, resolves local file paths relative
// to the configuration file, and validates required fields.
// 中文：Load 读取配置、应用安全默认值、相对配置文件解析路径，并校验必填字段。
func Load(path string) (Config, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Config{}, fmt.Errorf("inspect config: %w", err)
	}
	if runtime.GOOS != "windows" && permissionsBroaderThan(info.Mode().Perm(), 0o640) {
		return Config{}, errors.New("config permissions must be 0640 or stricter")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	cfg := Config{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("decode config: trailing JSON values are not allowed")
	}

	baseDir, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return Config{}, fmt.Errorf("resolve config directory: %w", err)
	}
	if cfg.Database.Path == "" {
		cfg.Database.Path = "data/audit.db"
	}
	cfg.Database.Path = resolvePath(baseDir, cfg.Database.Path)
	if cfg.Collector.Source == "" {
		cfg.Collector.Source = "mock"
	}
	if cfg.Collector.Interval == "" {
		cfg.Collector.Interval = defaultInterval.String()
	}
	if cfg.Collector.ConfigLockPath == "" {
		cfg.Collector.ConfigLockPath = "/run/vps-node-auditor/config.lock"
	}
	if !filepath.IsAbs(cfg.Collector.ConfigLockPath) {
		cfg.Collector.ConfigLockPath = resolvePath(baseDir, cfg.Collector.ConfigLockPath)
	}
	if cfg.Collector.MockFile != "" {
		cfg.Collector.MockFile = resolvePath(baseDir, cfg.Collector.MockFile)
	}
	if cfg.Collector.V2RayAPI.Address == "" {
		cfg.Collector.V2RayAPI.Address = "127.0.0.1:10085"
	}
	if cfg.Collector.V2RayAPI.Timeout == "" {
		cfg.Collector.V2RayAPI.Timeout = "5s"
	}
	if cfg.Collector.V2RayAPI.ProcessName == "" {
		cfg.Collector.V2RayAPI.ProcessName = "sing-box"
	}
	if cfg.Host.Interface == "" {
		cfg.Host.Interface = "eth0"
	}
	if cfg.Host.CommandTimeout == "" {
		cfg.Host.CommandTimeout = "5s"
	}
	if cfg.Display.Timezone == "" {
		cfg.Display.Timezone = "Local"
	}
	if cfg.Provider.CycleStartDay == 0 {
		cfg.Provider.CycleStartDay = 1
	}
	if cfg.Provider.TrafficDirection == "" {
		cfg.Provider.TrafficDirection = "both"
	}
	if cfg.Retention.MinuteDays == 0 {
		cfg.Retention.MinuteDays = 90
	}
	if cfg.Retention.EventDays == 0 {
		cfg.Retention.EventDays = 14
	}
	if cfg.Retention.RollupDays == 0 {
		cfg.Retention.RollupDays = 400
	}
	if cfg.Retention.BackupCount == 0 {
		cfg.Retention.BackupCount = 14
	}
	if len(cfg.Alerts.QuotaPercentages) == 0 {
		cfg.Alerts.QuotaPercentages = []float64{50, 75, 90, 95}
	}
	if cfg.Alerts.TrafficDifferencePct == 0 {
		cfg.Alerts.TrafficDifferencePct = 10
	}
	if cfg.Alerts.ConsecutiveFailures == 0 {
		cfg.Alerts.ConsecutiveFailures = 3
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func permissionsBroaderThan(actual, maximum os.FileMode) bool {
	return actual&^maximum != 0
}

// Validate checks values that would otherwise produce misleading audit data.
// 中文：Validate 拒绝可能造成错误或误导性审计结果的配置值。
func (c Config) Validate() error {
	if c.Database.Path == "" {
		return errors.New("database.path is required")
	}
	switch c.Collector.Source {
	case "mock":
		if c.Collector.MockFile == "" {
			return errors.New("collector.mock_file is required for the mock source")
		}
	case "sing-box-v2ray":
		if err := validateLoopbackAddress(c.Collector.V2RayAPI.Address); err != nil {
			return err
		}
		timeout, err := time.ParseDuration(c.Collector.V2RayAPI.Timeout)
		if err != nil || timeout <= 0 {
			return fmt.Errorf("collector.v2ray_api.timeout must be a positive duration: %q", c.Collector.V2RayAPI.Timeout)
		}
		if c.Collector.V2RayAPI.ProcessName == "" {
			return errors.New("collector.v2ray_api.process_name is required")
		}
	default:
		return fmt.Errorf("collector.source %q is not implemented", c.Collector.Source)
	}
	interval, err := time.ParseDuration(c.Collector.Interval)
	if err != nil || interval <= 0 {
		return fmt.Errorf("collector.interval must be a positive duration: %q", c.Collector.Interval)
	}
	if c.Host.CommandTimeout != "" {
		commandTimeout, err := time.ParseDuration(c.Host.CommandTimeout)
		if err != nil || commandTimeout <= 0 {
			return fmt.Errorf("host.command_timeout must be a positive duration: %q", c.Host.CommandTimeout)
		}
	}
	if strings.ContainsAny(c.Host.Interface, " /\\\t\r\n") {
		return errors.New("host.interface must be a simple interface name")
	}
	if c.Provider.CycleStartDay < 1 || c.Provider.CycleStartDay > 28 {
		return errors.New("provider.cycle_start_day must be between 1 and 28")
	}
	if c.Provider.MonthlyQuotaBytes < 0 {
		return errors.New("provider.monthly_quota_bytes must not be negative")
	}
	if c.Retention.MinuteDays < 0 || c.Retention.EventDays < 0 ||
		c.Retention.RollupDays < 0 || c.Retention.BackupCount < 0 {
		return errors.New("retention values must not be negative")
	}
	if c.Alerts.TrafficDifferencePct < 0 || c.Alerts.ConsecutiveFailures < 0 ||
		c.Alerts.UserHourlyBytes < 0 || c.Alerts.UserDailyBytes < 0 ||
		c.Alerts.HistoricalSpikeFactor < 0 {
		return errors.New("alert thresholds must not be negative")
	}
	previousQuota := float64(0)
	for _, threshold := range c.Alerts.QuotaPercentages {
		if threshold <= previousQuota || threshold > 100 {
			return errors.New("alerts.quota_percentages must be increasing values between 0 and 100")
		}
		previousQuota = threshold
	}
	switch c.Provider.TrafficDirection {
	case "both", "upload", "download", "max":
	default:
		return errors.New("provider.traffic_direction must be both, upload, download, or max")
	}
	if _, err := c.DisplayLocation(); err != nil {
		return err
	}
	if c.Provider.BillingTimezone != "" {
		if _, err := time.LoadLocation(c.Provider.BillingTimezone); err != nil {
			return fmt.Errorf("provider.billing_timezone: %w", err)
		}
	}
	seenUUIDs := make(map[string]struct{}, len(c.Credentials))
	seenNames := make(map[string]struct{}, len(c.Credentials))
	seenStatsUsers := make(map[string]struct{}, len(c.Credentials))
	for i, credential := range c.Credentials {
		if credential.UserName == "" || credential.CredentialName == "" || credential.UUID == "" {
			return fmt.Errorf("credentials[%d] requires user_name, credential_name, and uuid", i)
		}
		if !validUUID(credential.UUID) {
			return fmt.Errorf("credentials[%d].uuid must use RFC 4122 text format", i)
		}
		if _, exists := seenUUIDs[credential.UUID]; exists {
			return fmt.Errorf("credentials[%d] duplicates an earlier uuid", i)
		}
		seenUUIDs[credential.UUID] = struct{}{}
		nameKey := credential.UserName + "\x00" + credential.CredentialName
		if _, exists := seenNames[nameKey]; exists {
			return fmt.Errorf("credentials[%d] duplicates an earlier user and credential name", i)
		}
		seenNames[nameKey] = struct{}{}
		if c.Collector.Source == "sing-box-v2ray" && credential.Enabled {
			if credential.StatsUser == "" {
				return fmt.Errorf("credentials[%d].stats_user is required for sing-box-v2ray", i)
			}
			if strings.Contains(credential.StatsUser, ">>>") {
				return fmt.Errorf("credentials[%d].stats_user contains the reserved delimiter", i)
			}
			if _, exists := seenStatsUsers[credential.StatsUser]; exists {
				return fmt.Errorf("credentials[%d].stats_user duplicates an earlier enabled credential", i)
			}
			seenStatsUsers[credential.StatsUser] = struct{}{}
		}
	}
	return nil
}

// Interval returns the validated collector polling interval.
// 中文：Interval 返回已经通过校验的采集轮询周期。
func (c Config) Interval() time.Duration {
	interval, _ := time.ParseDuration(c.Collector.Interval)
	return interval
}

// CollectorTimeout returns the validated V2Ray API request timeout.
// 中文：CollectorTimeout 返回已经通过校验的 V2Ray API 请求超时时间。
func (c Config) CollectorTimeout() time.Duration {
	timeout, _ := time.ParseDuration(c.Collector.V2RayAPI.Timeout)
	return timeout
}

// HostCommandTimeout returns the validated timeout for each fixed host command.
// 中文：HostCommandTimeout 返回每条固定主机命令的已校验超时时间。
func (c Config) HostCommandTimeout() time.Duration {
	timeout, _ := time.ParseDuration(c.Host.CommandTimeout)
	return timeout
}

// DisplayLocation returns the configured output timezone.
// 中文：DisplayLocation 返回配置的输出显示时区。
func (c Config) DisplayLocation() (*time.Location, error) {
	location, err := time.LoadLocation(c.Display.Timezone)
	if err != nil {
		return nil, fmt.Errorf("display.timezone: %w", err)
	}
	return location, nil
}

// resolvePath makes runtime file locations independent of the caller's current
// working directory by resolving relative paths beside the configuration file.
// 中文：resolvePath 以配置文件目录为基准解析相对路径，避免依赖调用者当前目录。
func resolvePath(baseDir, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(baseDir, path)
}

// validUUID validates only RFC 4122 text structure. It intentionally avoids
// normalizing or returning the secret value in an error message.
// 中文：validUUID 只校验 RFC 4122 文本结构，不规范化 UUID，也不在错误中返回秘密值。
func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return err == nil && len(decoded) == 16
}

// validateLoopbackAddress prevents the unauthenticated Stats API client from
// being configured against a public or LAN endpoint by mistake.
// 中文：validateLoopbackAddress 强制使用回环地址，避免将无认证 Stats API 客户端
// 错误配置到公网或局域网端点。
func validateLoopbackAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return errors.New("collector.v2ray_api.address must be a loopback IP and port")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("collector.v2ray_api.address must use a loopback IP")
	}
	return nil
}
