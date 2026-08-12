// Package domain defines the data exchanged by the collector, storage, and
// command-line layers. It deliberately contains no database or transport code.
// 中文：domain 包定义采集器、存储和命令行层之间交换的数据，
// 并刻意不包含数据库或传输层实现。
package domain

import "time"

// Credential identifies one independently auditable proxy credential.
// 中文：Credential 表示一个可以独立审计的代理凭据。
type Credential struct {
	ID          int64
	UserID      int64
	UserName    string
	DisplayName string
	// UUID is an internal correlation key and must never be emitted in logs or
	// human-readable query results.
	// 中文：UUID 仅作为内部关联键，绝不能出现在日志或人工查询结果中。
	UUID      string
	StatsUser string
	Enabled   bool
	CreatedAt time.Time
	RevokedAt *time.Time
}

// Counters contains cumulative byte counters reported by a proxy core.
// 中文：Counters 保存代理核心报告的累计字节计数器。
type Counters struct {
	Upload   int64 `json:"upload_total"`
	Download int64 `json:"download_total"`
}

// SystemSnapshot contains host-wide evidence captured beside proxy counters.
// Network values are byte deltas for the current collection bucket.
// 中文：SystemSnapshot 保存与代理计数器同时采集的主机级证据；
// 网络数值表示当前采集桶内的字节增量。
type SystemSnapshot struct {
	CPUPercent       float64 `json:"cpu_percent"`
	Load1            float64 `json:"load_1"`
	MemoryUsedBytes  int64   `json:"memory_used_bytes"`
	SwapUsedBytes    int64   `json:"swap_used_bytes"`
	NetworkRXBytes   int64   `json:"network_rx_bytes"`
	NetworkTXBytes   int64   `json:"network_tx_bytes"`
	TCPRetransmits   int64   `json:"tcp_retransmits"`
	TCPResets        int64   `json:"tcp_resets"`
	InterfaceDrops   int64   `json:"interface_drops"`
	ActiveConnection int64   `json:"active_connections"`
	ProxyServiceOK   bool    `json:"proxy_service_ok"`
	CollectionOK     bool    `json:"collection_ok"`
	MissingSources   string  `json:"missing_sources,omitempty"`
}

// HostSnapshot contains raw host counters and validity flags. Storage owns the
// baselines so collector or host restarts cannot create artificial deltas.
// 中文：HostSnapshot 保存主机原始累计计数器及有效性标记；基线由存储层管理，
// 因此采集器或主机重启不会产生虚假增量。
type HostSnapshot struct {
	ObservedAt       time.Time
	HostBootID       string
	InterfaceName    string
	CPUTotal         int64
	CPUIdle          int64
	Load1            float64
	MemoryUsedBytes  int64
	SwapUsedBytes    int64
	NetworkRXTotal   int64
	NetworkTXTotal   int64
	InterfaceDrops   int64
	TCPRetransTotal  int64
	TCPResetsTotal   int64
	ActiveConnection int64
	ProxyServiceOK   bool
	CPUValid         bool
	MemoryValid      bool
	NetworkValid     bool
	TCPValid         bool
	ConnectionsValid bool
	ProxyValid       bool
	MissingSources   []string
	VnStat           *VnStatSnapshot
}

// VnStatSnapshot is one cumulative interface observation from vnStat.
// 中文：VnStatSnapshot 表示一次来自 vnStat 的网卡累计流量观测。
type VnStatSnapshot struct {
	InterfaceName string    `json:"interface_name"`
	RXTotal       int64     `json:"rx_total"`
	TXTotal       int64     `json:"tx_total"`
	CoverageStart time.Time `json:"coverage_start,omitempty"`
	CollectionOK  bool      `json:"collection_ok"`
}

// StatsSnapshot is one atomic observation from a statistics source.
// 中文：StatsSnapshot 表示统计数据源的一次原子观测。
type StatsSnapshot struct {
	// ObservedAt orders cumulative observations and rejects replayed snapshots.
	// 中文：ObservedAt 用于排序累计观测并拒绝重复快照。
	ObservedAt time.Time
	// CoreBootID changes only when a verified proxy process epoch changes.
	// 中文：CoreBootID 仅在已确认代理进程运行周期变化时改变。
	CoreBootID string
	// Users is keyed by the opaque credential UUID expected by storage.
	// 中文：Users 使用存储层所需的不透明凭据 UUID 作为内部键。
	Users map[string]Counters
	// System is optional because proxy traffic evidence can be collected alone.
	// 中文：System 可为空，因为代理流量证据可以单独采集。
	System *SystemSnapshot
}

// TopEntry is one ranked traffic result.
// 中文：TopEntry 表示流量排行中的一条结果。
type TopEntry struct {
	UserName       string  `json:"user_name"`
	CredentialName string  `json:"credential_name"`
	UploadBytes    int64   `json:"upload_bytes"`
	DownloadBytes  int64   `json:"download_bytes"`
	TotalBytes     int64   `json:"total_bytes"`
	SharePercent   float64 `json:"share_percent"`
}

// TimelineEntry represents credential activity in one minute bucket.
// 中文：TimelineEntry 表示凭据在一个分钟桶内的活动。
type TimelineEntry struct {
	BucketStart    time.Time `json:"bucket_start"`
	UploadBytes    int64     `json:"upload_bytes"`
	DownloadBytes  int64     `json:"download_bytes"`
	Active         bool      `json:"active"`
	CollectionOK   bool      `json:"collection_ok"`
	ResetDetected  bool      `json:"reset_detected"`
	EvidenceSource string    `json:"source"`
}

// TimelineBucket identifies a stable query and rollup granularity.
// 中文：TimelineBucket 标识稳定的查询和汇总粒度。
type TimelineBucket string

const (
	BucketMinute TimelineBucket = "minute"
	BucketHour   TimelineBucket = "hour"
	BucketDay    TimelineBucket = "day"
	BucketMonth  TimelineBucket = "month"
)

// Event is a redacted operational event stored by the collector.
// 中文：Event 表示采集器保存的一条已脱敏运行事件。
type Event struct {
	OccurredAt     time.Time `json:"occurred_at"`
	CredentialName string    `json:"credential_name,omitempty"`
	Severity       string    `json:"severity"`
	Category       string    `json:"category"`
	Message        string    `json:"message"`
}

// SystemSample is a timestamped host-wide metric sample.
// 中文：SystemSample 表示带时间戳的主机级指标样本。
type SystemSample struct {
	BucketStart time.Time `json:"bucket_start"`
	SystemSnapshot
}

// Health summarizes the freshness and integrity signals available locally.
// 中文：Health 汇总本地可获得的数据新鲜度和完整性信号。
type Health struct {
	DatabaseOK          bool       `json:"database_ok"`
	LastTrafficSampleAt *time.Time `json:"last_traffic_sample_at,omitempty"`
	LastSystemSampleAt  *time.Time `json:"last_system_sample_at,omitempty"`
	RecentFailures      int64      `json:"recent_failures"`
	EnabledCredentials  int64      `json:"enabled_credentials"`
	ConsecutiveFailures int64      `json:"consecutive_failures"`
	StatsAPIOK          bool       `json:"stats_api_ok"`
	VnStatOK            bool       `json:"vnstat_ok"`
	ProxyServiceOK      bool       `json:"proxy_service_ok"`
	TrafficFresh        bool       `json:"traffic_fresh"`
	SystemFresh         bool       `json:"system_fresh"`
	SystemCollectionOK  bool       `json:"system_collection_ok"`
	SystemMissing       string     `json:"system_missing_sources,omitempty"`
}

// MonthUsage compares proxy-user evidence with the independent vnStat view.
// 中文：MonthUsage 并列比较代理用户证据与独立的 vnStat 网卡口径。
type MonthUsage struct {
	UserUploadBytes     int64      `json:"user_upload_bytes"`
	UserDownloadBytes   int64      `json:"user_download_bytes"`
	UserTotalBytes      int64      `json:"user_total_bytes"`
	CoveredUserBytes    int64      `json:"covered_user_total_bytes"`
	InterfaceRXBytes    int64      `json:"interface_rx_bytes"`
	InterfaceTXBytes    int64      `json:"interface_tx_bytes"`
	InterfaceTotalBytes int64      `json:"interface_total_bytes"`
	DifferenceBytes     int64      `json:"difference_bytes"`
	DifferencePercent   float64    `json:"difference_percent"`
	CoverageStart       *time.Time `json:"coverage_start,omitempty"`
	VnStatAvailable     bool       `json:"vnstat_available"`
}

// CredentialSummary is safe for list output and intentionally excludes UUID.
// 中文：CredentialSummary 用于安全列表输出，并刻意不包含 UUID。
type CredentialSummary struct {
	UserName       string     `json:"user_name"`
	CredentialName string     `json:"credential_name"`
	Enabled        bool       `json:"enabled"`
	CreatedAt      time.Time  `json:"created_at"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
}
