package storage

// migration is one atomic, append-only SQLite schema change.
// 中文：migration 表示一次原子、只追加的 SQLite 结构变更。
type migration struct {
	Version int
	SQL     string
}

// migrations are deliberately versioned instead of relying on a single
// CREATE IF NOT EXISTS script. Production upgrades can therefore prove which
// changes were applied and safely resume after an interrupted deployment.
// 中文：迁移采用显式版本，而不是单个 CREATE IF NOT EXISTS 脚本；生产升级可据此
// 确认已应用的变更，并能在部署中断后安全继续。
var migrations = []migration{
	{Version: 1, SQL: `
CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY,
    display_name TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS credentials (
    id INTEGER PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id),
    display_name TEXT NOT NULL,
    uuid TEXT NOT NULL UNIQUE,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    revoked_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_credentials_user_id ON credentials(user_id);

CREATE TABLE IF NOT EXISTS traffic_samples (
    id INTEGER PRIMARY KEY,
    bucket_start INTEGER NOT NULL,
    credential_id INTEGER NOT NULL REFERENCES credentials(id),
    upload_delta INTEGER NOT NULL,
    download_delta INTEGER NOT NULL,
    active INTEGER NOT NULL,
    source TEXT NOT NULL,
    collection_ok INTEGER NOT NULL,
    reset_detected INTEGER NOT NULL DEFAULT 0,
    UNIQUE(bucket_start, credential_id, source)
);

CREATE INDEX IF NOT EXISTS idx_traffic_samples_time ON traffic_samples(bucket_start);
CREATE INDEX IF NOT EXISTS idx_traffic_samples_credential_time
    ON traffic_samples(credential_id, bucket_start);

CREATE TABLE IF NOT EXISTS system_samples (
    id INTEGER PRIMARY KEY,
    bucket_start INTEGER NOT NULL,
    source TEXT NOT NULL,
    cpu REAL NOT NULL,
    load_1 REAL NOT NULL,
    memory_used INTEGER NOT NULL,
    swap_used INTEGER NOT NULL,
    network_rx INTEGER NOT NULL,
    network_tx INTEGER NOT NULL,
    tcp_retrans INTEGER NOT NULL,
    tcp_resets INTEGER NOT NULL,
    interface_drops INTEGER NOT NULL,
    active_connections INTEGER NOT NULL,
    proxy_service_ok INTEGER NOT NULL,
    UNIQUE(bucket_start, source)
);

CREATE INDEX IF NOT EXISTS idx_system_samples_time ON system_samples(bucket_start);

CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY,
    occurred_at INTEGER NOT NULL,
    credential_id INTEGER REFERENCES credentials(id),
    severity TEXT NOT NULL,
    category TEXT NOT NULL,
    redacted_message TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_events_time ON events(occurred_at);

CREATE TABLE IF NOT EXISTS collector_state (
    source TEXT NOT NULL,
    credential_id INTEGER NOT NULL REFERENCES credentials(id),
    last_upload_counter INTEGER NOT NULL,
    last_download_counter INTEGER NOT NULL,
    last_success_at INTEGER NOT NULL,
    core_boot_id TEXT NOT NULL,
    PRIMARY KEY(source, credential_id)
);
`},
	{Version: 2, SQL: `
ALTER TABLE credentials ADD COLUMN stats_user TEXT;
ALTER TABLE system_samples ADD COLUMN collection_ok INTEGER NOT NULL DEFAULT 1;
ALTER TABLE system_samples ADD COLUMN missing_sources TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_credentials_stats_user
    ON credentials(stats_user) WHERE stats_user IS NOT NULL AND stats_user <> '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_credentials_user_display
    ON credentials(user_id, display_name);

CREATE TABLE IF NOT EXISTS system_collector_state (
    source TEXT NOT NULL,
    metric_name TEXT NOT NULL,
    last_value INTEGER NOT NULL,
    last_success_at INTEGER NOT NULL,
    host_boot_id TEXT NOT NULL,
    PRIMARY KEY(source, metric_name)
);

CREATE TABLE IF NOT EXISTS vnstat_snapshots (
    id INTEGER PRIMARY KEY,
    observed_at INTEGER NOT NULL,
    interface_name TEXT NOT NULL,
    rx_total INTEGER NOT NULL,
    tx_total INTEGER NOT NULL,
    coverage_start INTEGER,
    collection_ok INTEGER NOT NULL,
    UNIQUE(observed_at, interface_name)
);
CREATE INDEX IF NOT EXISTS idx_vnstat_snapshots_time ON vnstat_snapshots(observed_at);

CREATE TABLE IF NOT EXISTS traffic_rollups (
    bucket_kind TEXT NOT NULL CHECK(bucket_kind IN ('hour', 'day', 'month')),
    bucket_start INTEGER NOT NULL,
    credential_id INTEGER NOT NULL REFERENCES credentials(id),
    upload_bytes INTEGER NOT NULL,
    download_bytes INTEGER NOT NULL,
    active_buckets INTEGER NOT NULL,
    sample_buckets INTEGER NOT NULL,
    gap_buckets INTEGER NOT NULL,
    reset_count INTEGER NOT NULL,
    PRIMARY KEY(bucket_kind, bucket_start, credential_id)
);
CREATE INDEX IF NOT EXISTS idx_traffic_rollups_time
    ON traffic_rollups(bucket_kind, bucket_start);

CREATE TABLE IF NOT EXISTS system_rollups (
    bucket_kind TEXT NOT NULL CHECK(bucket_kind IN ('hour', 'day', 'month')),
    bucket_start INTEGER NOT NULL,
    sample_count INTEGER NOT NULL,
    gap_count INTEGER NOT NULL,
    cpu_avg REAL NOT NULL,
    cpu_max REAL NOT NULL,
    load_1_avg REAL NOT NULL,
    load_1_max REAL NOT NULL,
    memory_used_max INTEGER NOT NULL,
    swap_used_max INTEGER NOT NULL,
    network_rx INTEGER NOT NULL,
    network_tx INTEGER NOT NULL,
    tcp_retrans INTEGER NOT NULL,
    tcp_resets INTEGER NOT NULL,
    interface_drops INTEGER NOT NULL,
    active_connections_max INTEGER NOT NULL,
    proxy_service_failures INTEGER NOT NULL,
    PRIMARY KEY(bucket_kind, bucket_start)
);

CREATE TABLE IF NOT EXISTS alert_state (
    alert_key TEXT PRIMARY KEY,
    active INTEGER NOT NULL,
    first_triggered_at INTEGER,
    last_evaluated_at INTEGER NOT NULL,
    last_value REAL NOT NULL,
    last_message TEXT NOT NULL
);
`},
	{Version: 3, SQL: `
DROP INDEX IF EXISTS idx_credentials_user_display;
CREATE UNIQUE INDEX idx_credentials_user_display
    ON credentials(user_id, display_name) WHERE enabled = 1;
`},
}
