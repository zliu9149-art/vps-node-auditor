// Package storage persists audit data in one local SQLite database.
// 中文：storage 包将审计数据持久化到一个本地 SQLite 数据库。
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	_ "modernc.org/sqlite"

	"vps-node-auditor/internal/config"
	"vps-node-auditor/internal/domain"
)

// ErrStaleSnapshot indicates that a source returned an observation that was
// already recorded. Rejecting it prevents duplicate traffic accounting.
// 中文：ErrStaleSnapshot 表示数据源返回了已记录观测；拒绝它可防止重复计算流量。
var ErrStaleSnapshot = errors.New("snapshot is not newer than collector state")

// Store provides transactional access to the local audit database.
// 中文：Store 提供对本地审计数据库的事务访问。
type Store struct {
	db       *sql.DB
	path     string
	readOnly bool
}

// Open opens the SQLite database at path. Writable databases are created with
// private permissions and WAL mode; read-only callers cannot run migrations.
// 中文：Open 打开指定 SQLite 数据库；可写数据库使用私有权限和 WAL 模式，
// 只读调用方不能执行迁移。
func Open(path string, readOnly bool) (*Store, error) {
	if path == "" {
		return nil, errors.New("database path is required")
	}
	if !readOnly && path != ":memory:" {
		directory := filepath.Dir(path)
		_, statErr := os.Stat(directory)
		directoryCreated := errors.Is(statErr, os.ErrNotExist)
		if statErr != nil && !directoryCreated {
			return nil, fmt.Errorf("inspect database directory: %w", statErr)
		}
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
		// Never chmod an existing parent such as /var/lib. Installation owns
		// existing-directory policy; Open only hardens a directory it created.
		// 中文：绝不修改 /var/lib 等现有父目录权限；安装流程负责既有目录策略，
		// Open 只加固由自己创建的目录。
		if directoryCreated {
			if err := os.Chmod(directory, 0o700); err != nil {
				return nil, fmt.Errorf("restrict database directory permissions: %w", err)
			}
		}
	}

	dsn := "file:" + filepath.ToSlash(path)
	if readOnly {
		dsn += "?mode=ro"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// A single connection makes PRAGMA settings deterministic and is sufficient
	// for the low-volume, one-writer workload described by the requirements.
	// 中文：单连接可确保 PRAGMA 行为确定，且足以满足低流量、单写入者工作负载。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &Store{db: db, path: path, readOnly: readOnly}
	if err := store.configure(readOnly); err != nil {
		db.Close()
		return nil, err
	}
	if !readOnly && path != ":memory:" {
		if err := secureDatabaseFiles(path); err != nil {
			db.Close()
			return nil, err
		}
	}
	return store, nil
}

// configure applies connection-local safety and concurrency settings. Read-only
// clients are additionally prevented from issuing accidental writes.
// 中文：configure 应用连接级安全和并发设置，并进一步禁止只读客户端意外写入。
func (s *Store) configure(readOnly bool) error {
	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	}
	if readOnly {
		pragmas = append(pragmas, "PRAGMA query_only = ON")
	} else {
		pragmas = append(pragmas, "PRAGMA journal_mode = WAL", "PRAGMA wal_autocheckpoint = 1000")
	}
	for _, statement := range pragmas {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("configure sqlite: %w", err)
		}
	}
	return nil
}

// Close closes the underlying database connection.
// 中文：Close 关闭底层数据库连接。
func (s *Store) Close() error {
	return s.db.Close()
}

// Migrate applies every pending schema version in its own transaction without
// deleting existing audit data.
// 中文：Migrate 在独立事务中应用每个待执行版本，且不删除既有审计数据。
func (s *Store) Migrate(ctx context.Context) error {
	if s.readOnly {
		return errors.New("cannot migrate a read-only database")
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}
	var current int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&current); err != nil {
		return fmt.Errorf("read migration version: %w", err)
	}
	for _, item := range migrations {
		if item.Version <= current {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", item.Version, err)
		}
		if _, err := tx.ExecContext(ctx, item.SQL); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", item.Version, err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)",
			item.Version, time.Now().UTC().Unix()); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d: %w", item.Version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", item.Version, err)
		}
	}
	return s.secureFiles()
}

// Ping verifies that the database is readable.
// 中文：Ping 验证数据库是否可读。
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// SyncCredentials inserts or updates the manually configured credentials used
// during phase one. It never logs or returns UUID values.
// 中文：SyncCredentials 插入或更新第一阶段的人工配置凭据，绝不记录或返回 UUID。
func (s *Store) SyncCredentials(ctx context.Context, credentials []config.CredentialConfig) error {
	// User and credential rows must move together: a partial sync could attach
	// later traffic samples to the wrong display identity.
	// 中文：用户与凭据必须在同一事务中更新，部分同步可能把后续样本关联到错误身份。
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin credential sync: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC().Unix()
	for _, item := range credentials {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO users(display_name, created_at) VALUES(?, ?)
			 ON CONFLICT(display_name) DO NOTHING`, item.UserName, now); err != nil {
			return fmt.Errorf("ensure user: %w", err)
		}
		var userID int64
		if err := tx.QueryRowContext(ctx,
			"SELECT id FROM users WHERE display_name = ?", item.UserName).Scan(&userID); err != nil {
			return fmt.Errorf("lookup user: %w", err)
		}
		enabled := boolInt(item.Enabled)
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO credentials(user_id, display_name, uuid, stats_user, enabled, created_at)
			 VALUES(?, ?, ?, NULLIF(?, ''), ?, ?)
			 ON CONFLICT(uuid) DO UPDATE SET
			   user_id = excluded.user_id,
			   display_name = excluded.display_name,
			   stats_user = COALESCE(NULLIF(excluded.stats_user, ''), credentials.stats_user)`,
			userID, item.CredentialName, item.UUID, item.StatsUser, enabled, now); err != nil {
			return fmt.Errorf("ensure credential: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit credential sync: %w", err)
	}
	return s.secureFiles()
}

// RecordSnapshot atomically converts cumulative proxy counters into minute
// deltas, records optional system evidence, and advances collector state.
// 中文：RecordSnapshot 原子地将代理累计计数器转换为分钟增量，
// 同时记录可选系统证据并推进采集状态。
func (s *Store) RecordSnapshot(ctx context.Context, source string, snapshot domain.StatsSnapshot) error {
	if snapshot.ObservedAt.IsZero() {
		return errors.New("snapshot observed time is required")
	}
	observedAt := snapshot.ObservedAt.UTC()
	bucket := observedAt.Truncate(time.Minute)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin snapshot: %w", err)
	}
	defer tx.Rollback()

	var latest sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		"SELECT MAX(last_success_at) FROM collector_state WHERE source = ?", source).Scan(&latest); err != nil {
		return fmt.Errorf("read latest collector state: %w", err)
	}
	if latest.Valid && observedAt.Unix() <= latest.Int64 {
		// Replayed or out-of-order cumulative counters would otherwise be
		// subtracted twice and inflate the audit total.
		// 中文：重复或乱序累计计数器会被重复相减并夸大审计总量，因此必须拒绝。
		return ErrStaleSnapshot
	}

	credentials, err := listEnabledCredentials(ctx, tx)
	if err != nil {
		return err
	}
	for _, credential := range credentials {
		counters, found := snapshot.Users[credential.UUID]
		if !found {
			// Missing is evidence of an incomplete collection, not zero traffic.
			// Persist a gap so later queries cannot silently treat it as success.
			// 中文：缺失计数器表示采集不完整，而不是零流量；必须保存缺口，
			// 防止后续查询静默地将其当作成功。
			if err := insertTrafficSample(ctx, tx, bucket, credential.ID, 0, 0, source, false, false); err != nil {
				return err
			}
			if err := insertEvent(ctx, tx, observedAt, &credential.ID, "warning", "missing_counter", "statistics source omitted an enabled credential"); err != nil {
				return err
			}
			continue
		}

		previous, found, err := readCollectorState(ctx, tx, source, credential.ID)
		if err != nil {
			return err
		}
		uploadDelta, downloadDelta, ok, reset := calculateDelta(previous, found, counters, snapshot.CoreBootID)
		if err := insertTrafficSample(ctx, tx, bucket, credential.ID, uploadDelta, downloadDelta, source, ok, reset); err != nil {
			return err
		}
		if reset {
			message := "proxy counters reset; the bucket was rebased from verified restart counters"
			if !ok {
				message = "proxy counters regressed without a verified restart; the bucket was marked as a gap"
			}
			if err := insertEvent(ctx, tx, observedAt, &credential.ID, "warning", "counter_reset", message); err != nil {
				return err
			}
		}
		if err := upsertCollectorState(ctx, tx, source, credential.ID, counters, observedAt, snapshot.CoreBootID); err != nil {
			return err
		}
	}

	if snapshot.System != nil {
		if err := insertSystemSample(ctx, tx, bucket, source, *snapshot.System); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit snapshot: %w", err)
	}
	return s.secureFiles()
}

// RecordFailure marks a collection gap for every enabled credential without
// changing the previous cumulative counters.
// 中文：RecordFailure 为每个启用凭据记录采集缺口，但不修改上次累计计数器。
func (s *Store) RecordFailure(ctx context.Context, source string, observedAt time.Time) error {
	observedAt = observedAt.UTC()
	bucket := observedAt.Truncate(time.Minute)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin failure record: %w", err)
	}
	defer tx.Rollback()
	credentials, err := listEnabledCredentials(ctx, tx)
	if err != nil {
		return err
	}
	for _, credential := range credentials {
		if err := insertTrafficSample(ctx, tx, bucket, credential.ID, 0, 0, source, false, false); err != nil {
			return err
		}
	}
	if err := insertEvent(ctx, tx, observedAt, nil, "error", "collection_failed", "statistics collection failed; details are available in the local service journal"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit failure record: %w", err)
	}
	return s.secureFiles()
}

// Top returns registered-user traffic totals ranked by total bytes. Traffic
// from rotated or device-specific credentials is aggregated to the user.
// 中文：Top 按总字节数返回注册用户排行；轮换或设备专用凭据流量合并到所属用户。
func (s *Store) Top(ctx context.Context, from, to time.Time) ([]domain.TopEntry, error) {
	return s.top(ctx, from, to, false)
}

// CredentialTop returns independently auditable credential totals.
// 中文：CredentialTop 返回可独立审计的凭据流量总计。
func (s *Store) CredentialTop(ctx context.Context, from, to time.Time) ([]domain.TopEntry, error) {
	return s.top(ctx, from, to, true)
}

func (s *Store) top(ctx context.Context, from, to time.Time, byCredential bool) ([]domain.TopEntry, error) {
	selectClause := `u.display_name, '',
		       COALESCE(SUM(t.upload_delta), 0),
		       COALESCE(SUM(t.download_delta), 0)`
	groupClause := "u.id, u.display_name"
	if byCredential {
		selectClause = `u.display_name, c.display_name,
		       COALESCE(SUM(t.upload_delta), 0),
		       COALESCE(SUM(t.download_delta), 0)`
		groupClause = "c.id, u.display_name, c.display_name"
	}
	fromUnix, toUnix := from.UTC().Unix(), to.UTC().Unix()
	rows, err := s.db.QueryContext(ctx, `
		WITH selected_traffic AS (
		  SELECT t.credential_id, t.upload_delta, t.download_delta
		  FROM traffic_samples t
		  WHERE t.bucket_start >= ? AND t.bucket_start < ? AND t.collection_ok = 1
		    AND NOT EXISTS (
		      SELECT 1 FROM traffic_rollups r
		      WHERE r.bucket_kind='hour' AND r.credential_id=t.credential_id
		        AND t.bucket_start>=r.bucket_start AND t.bucket_start<r.bucket_start+3600
		        AND r.bucket_start>=? AND r.bucket_start+3600<=?
		    )
		  UNION ALL
		  SELECT r.credential_id, r.upload_bytes, r.download_bytes
		  FROM traffic_rollups r
		  WHERE r.bucket_kind='hour' AND r.bucket_start>=? AND r.bucket_start+3600<=?
		)
		SELECT `+selectClause+`
		FROM selected_traffic t
		JOIN credentials c ON c.id = t.credential_id
		JOIN users u ON u.id = c.user_id
		GROUP BY `+groupClause,
		fromUnix, toUnix, fromUnix, toUnix, fromUnix, toUnix)
	if err != nil {
		return nil, fmt.Errorf("query top traffic: %w", err)
	}
	defer rows.Close()

	var entries []domain.TopEntry
	var grandTotal int64
	for rows.Next() {
		var entry domain.TopEntry
		if err := rows.Scan(&entry.UserName, &entry.CredentialName, &entry.UploadBytes, &entry.DownloadBytes); err != nil {
			return nil, fmt.Errorf("scan top traffic: %w", err)
		}
		entry.TotalBytes = entry.UploadBytes + entry.DownloadBytes
		grandTotal += entry.TotalBytes
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate top traffic: %w", err)
	}
	for i := range entries {
		if grandTotal > 0 {
			entries[i].SharePercent = float64(entries[i].TotalBytes) * 100 / float64(grandTotal)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].TotalBytes > entries[j].TotalBytes })
	return entries, nil
}

// Timeline returns minute-level activity for a registered user or credential
// display name. Multiple credentials are aggregated for a user-name query.
// 中文：Timeline 按注册用户名或凭据显示名返回分钟级活动；
// 按用户名查询时会聚合该用户的多个凭据。
func (s *Store) Timeline(ctx context.Context, name string, from, to time.Time) ([]domain.TimelineEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.bucket_start,
		       COALESCE(SUM(t.upload_delta), 0),
		       COALESCE(SUM(t.download_delta), 0),
		       MAX(t.active), MIN(t.collection_ok), MAX(t.reset_detected),
		       GROUP_CONCAT(DISTINCT t.source)
		FROM traffic_samples t
		JOIN credentials c ON c.id = t.credential_id
		JOIN users u ON u.id = c.user_id
		WHERE (u.display_name = ? OR c.display_name = ?)
		  AND t.bucket_start >= ? AND t.bucket_start < ?
		GROUP BY t.bucket_start
		ORDER BY t.bucket_start`, name, name, from.UTC().Unix(), to.UTC().Unix())
	if err != nil {
		return nil, fmt.Errorf("query user timeline: %w", err)
	}
	defer rows.Close()
	var result []domain.TimelineEntry
	for rows.Next() {
		var item domain.TimelineEntry
		var bucket int64
		var active, ok, reset int
		if err := rows.Scan(&bucket, &item.UploadBytes, &item.DownloadBytes, &active, &ok, &reset, &item.EvidenceSource); err != nil {
			return nil, fmt.Errorf("scan user timeline: %w", err)
		}
		item.BucketStart = time.Unix(bucket, 0).UTC()
		item.Active = active != 0
		item.CollectionOK = ok != 0
		item.ResetDetected = reset != 0
		result = append(result, item)
	}
	return result, rows.Err()
}

// Events returns redacted operational events in chronological order.
// 中文：Events 按时间顺序返回已脱敏的运行事件。
func (s *Store) Events(ctx context.Context, from, to time.Time) ([]domain.Event, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.occurred_at, COALESCE(c.display_name, ''), e.severity,
		       e.category, e.redacted_message
		FROM events e
		LEFT JOIN credentials c ON c.id = e.credential_id
		WHERE e.occurred_at >= ? AND e.occurred_at < ?
		ORDER BY e.occurred_at`, from.UTC().Unix(), to.UTC().Unix())
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()
	var result []domain.Event
	for rows.Next() {
		var item domain.Event
		var occurredAt int64
		if err := rows.Scan(&occurredAt, &item.CredentialName, &item.Severity, &item.Category, &item.Message); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		item.OccurredAt = time.Unix(occurredAt, 0).UTC()
		result = append(result, item)
	}
	return result, rows.Err()
}

// SystemSamples returns host-wide evidence in chronological order.
// 中文：SystemSamples 按时间顺序返回主机级证据样本。
func (s *Store) SystemSamples(ctx context.Context, from, to time.Time) ([]domain.SystemSample, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT bucket_start, cpu, load_1, memory_used, swap_used,
		       network_rx, network_tx, tcp_retrans, tcp_resets,
		       interface_drops, active_connections, proxy_service_ok,
		       collection_ok, missing_sources
		FROM system_samples
		WHERE bucket_start >= ? AND bucket_start < ?
		ORDER BY bucket_start`, from.UTC().Unix(), to.UTC().Unix())
	if err != nil {
		return nil, fmt.Errorf("query system samples: %w", err)
	}
	defer rows.Close()
	var result []domain.SystemSample
	for rows.Next() {
		var item domain.SystemSample
		var bucket int64
		var proxyOK, collectionOK int
		if err := rows.Scan(&bucket, &item.CPUPercent, &item.Load1, &item.MemoryUsedBytes,
			&item.SwapUsedBytes, &item.NetworkRXBytes, &item.NetworkTXBytes,
			&item.TCPRetransmits, &item.TCPResets, &item.InterfaceDrops,
			&item.ActiveConnection, &proxyOK, &collectionOK, &item.MissingSources); err != nil {
			return nil, fmt.Errorf("scan system sample: %w", err)
		}
		item.BucketStart = time.Unix(bucket, 0).UTC()
		item.ProxyServiceOK = proxyOK != 0
		item.CollectionOK = collectionOK != 0
		result = append(result, item)
	}
	return result, rows.Err()
}

// Health returns local freshness and recent collection-failure indicators.
// 中文：Health 返回本地数据新鲜度和近期采集失败指标。
func (s *Store) Health(ctx context.Context, now time.Time) (domain.Health, error) {
	health := domain.Health{DatabaseOK: true}
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM credentials WHERE enabled = 1").Scan(&health.EnabledCredentials); err != nil {
		return domain.Health{}, fmt.Errorf("count credentials: %w", err)
	}
	if err := scanOptionalTime(s.db.QueryRowContext(ctx, "SELECT MAX(bucket_start) FROM traffic_samples"), &health.LastTrafficSampleAt); err != nil {
		return domain.Health{}, err
	}
	if err := scanOptionalTime(s.db.QueryRowContext(ctx, "SELECT MAX(bucket_start) FROM system_samples"), &health.LastSystemSampleAt); err != nil {
		return domain.Health{}, err
	}
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM events WHERE category = 'collection_failed' AND occurred_at >= ?",
		now.UTC().Add(-24*time.Hour).Unix()).Scan(&health.RecentFailures); err != nil {
		return domain.Health{}, fmt.Errorf("count recent failures: %w", err)
	}
	consecutive, err := s.consecutiveFailures(ctx)
	if err != nil {
		return domain.Health{}, err
	}
	health.ConsecutiveFailures = consecutive
	var trafficOK sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(collection_ok) FROM traffic_samples
		WHERE bucket_start=(SELECT MAX(bucket_start) FROM traffic_samples)`).Scan(&trafficOK); err != nil {
		return domain.Health{}, fmt.Errorf("read latest traffic quality: %w", err)
	}
	health.StatsAPIOK = trafficOK.Valid && trafficOK.Int64 != 0
	var proxyOK, systemOK sql.NullInt64
	var missing sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT proxy_service_ok, collection_ok, missing_sources
		FROM system_samples ORDER BY bucket_start DESC LIMIT 1`).Scan(&proxyOK, &systemOK, &missing); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return domain.Health{}, fmt.Errorf("read latest system quality: %w", err)
	}
	health.ProxyServiceOK = proxyOK.Valid && proxyOK.Int64 != 0
	health.SystemCollectionOK = systemOK.Valid && systemOK.Int64 != 0
	if missing.Valid {
		health.SystemMissing = missing.String
	}
	var lastVnStat sql.NullInt64
	if err := s.db.QueryRowContext(ctx, "SELECT MAX(observed_at) FROM vnstat_snapshots WHERE collection_ok=1").Scan(&lastVnStat); err != nil {
		return domain.Health{}, fmt.Errorf("read vnStat freshness: %w", err)
	}
	freshCutoff := now.UTC().Add(-3 * time.Minute)
	health.VnStatOK = lastVnStat.Valid && time.Unix(lastVnStat.Int64, 0).After(freshCutoff)
	health.TrafficFresh = health.LastTrafficSampleAt != nil && health.LastTrafficSampleAt.After(freshCutoff)
	health.SystemFresh = health.LastSystemSampleAt != nil && health.LastSystemSampleAt.After(freshCutoff)
	return health, nil
}

// collectorState is the last accepted cumulative observation for one source
// and credential pair.
// 中文：collectorState 保存一个数据源与凭据组合最后一次被接受的累计观测。
type collectorState struct {
	Upload      int64
	Download    int64
	LastSuccess time.Time
	CoreBootID  string
}

// calculateDelta returns upload, download, collection validity, and whether a
// reset or regression was detected. A reset is billable only when process
// identity proves that the proxy entered a new epoch.
// 中文：calculateDelta 返回上行、下行、采集有效性及是否检测到重置或回退；
// 只有进程身份能够证明代理进入新周期时，重置后的计数才可计入流量。
func calculateDelta(previous collectorState, found bool, current domain.Counters, coreBootID string) (int64, int64, bool, bool) {
	if !found {
		// The first observation establishes a baseline. Counting the entire
		// cumulative value would misattribute traffic from before installation.
		// 中文：首次观测只建立基线；若计入全部累计值，会错误归属安装前的流量。
		return 0, 0, true, false
	}
	bootChanged := previous.CoreBootID != "" && coreBootID != "" && previous.CoreBootID != coreBootID
	if bootChanged {
		// A verified restart means the current counters represent traffic since
		// the restart and can safely become the first delta of the new epoch.
		// 中文：已确认重启说明当前计数器代表重启后流量，可安全作为新周期首个增量。
		return current.Upload, current.Download, true, true
	}
	if current.Upload < previous.Upload || current.Download < previous.Download {
		// Without a verified process change, rebasing would turn an unexplained
		// regression into apparently valid traffic. Record a gap instead.
		// 中文：没有已确认进程变化时，重新设基线会把无法解释的回退伪装成有效流量，
		// 因此应记录缺口。
		return 0, 0, false, true
	}
	return current.Upload - previous.Upload, current.Download - previous.Download, true, false
}

// listEnabledCredentials returns the correlation keys needed inside the active
// transaction. Callers must not log the returned UUID values.
// 中文：listEnabledCredentials 返回当前事务所需关联键；调用方不得记录返回的 UUID。
func listEnabledCredentials(ctx context.Context, tx *sql.Tx) ([]domain.Credential, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT c.id, c.user_id, u.display_name, c.display_name, c.uuid,
		       c.enabled, c.created_at
		FROM credentials c JOIN users u ON u.id = c.user_id
		WHERE c.enabled = 1 ORDER BY c.id`)
	if err != nil {
		return nil, fmt.Errorf("list enabled credentials: %w", err)
	}
	defer rows.Close()
	var result []domain.Credential
	for rows.Next() {
		var item domain.Credential
		var enabled int
		var createdAt int64
		if err := rows.Scan(&item.ID, &item.UserID, &item.UserName, &item.DisplayName,
			&item.UUID, &enabled, &createdAt); err != nil {
			return nil, fmt.Errorf("scan credential: %w", err)
		}
		item.Enabled = enabled != 0
		item.CreatedAt = time.Unix(createdAt, 0).UTC()
		result = append(result, item)
	}
	return result, rows.Err()
}

// readCollectorState loads the cumulative baseline for delta calculation.
// 中文：readCollectorState 加载用于增量计算的累计基线。
func readCollectorState(ctx context.Context, tx *sql.Tx, source string, credentialID int64) (collectorState, bool, error) {
	var state collectorState
	var lastSuccess int64
	err := tx.QueryRowContext(ctx, `
		SELECT last_upload_counter, last_download_counter, last_success_at, core_boot_id
		FROM collector_state WHERE source = ? AND credential_id = ?`, source, credentialID).
		Scan(&state.Upload, &state.Download, &lastSuccess, &state.CoreBootID)
	if errors.Is(err, sql.ErrNoRows) {
		return collectorState{}, false, nil
	}
	if err != nil {
		return collectorState{}, false, fmt.Errorf("read collector state: %w", err)
	}
	state.LastSuccess = time.Unix(lastSuccess, 0).UTC()
	return state, true, nil
}

// insertTrafficSample accumulates repeated observations in the same minute and
// preserves the worst evidence flags so a later success cannot erase a gap.
// 中文：insertTrafficSample 累加同一分钟内的重复观测，并保留最差证据标志，
// 防止后续成功观测抹除已有缺口。
func insertTrafficSample(ctx context.Context, tx *sql.Tx, bucket time.Time, credentialID, upload, download int64, source string, ok, reset bool) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO traffic_samples(
		  bucket_start, credential_id, upload_delta, download_delta,
		  active, source, collection_ok, reset_detected
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(bucket_start, credential_id, source) DO UPDATE SET
		  upload_delta = traffic_samples.upload_delta + excluded.upload_delta,
		  download_delta = traffic_samples.download_delta + excluded.download_delta,
		  active = MAX(traffic_samples.active, excluded.active),
		  collection_ok = MIN(traffic_samples.collection_ok, excluded.collection_ok),
		  reset_detected = MAX(traffic_samples.reset_detected, excluded.reset_detected)`,
		bucket.Unix(), credentialID, upload, download,
		boolInt(upload+download > 0), source, boolInt(ok), boolInt(reset))
	if err != nil {
		return fmt.Errorf("insert traffic sample: %w", err)
	}
	return nil
}

// insertSystemSample replaces point-in-time gauges, accumulates interval
// counters, and preserves any proxy-service failure within the minute.
// 中文：insertSystemSample 替换时点指标、累加区间计数器，并保留该分钟内任一代理服务失败。
func insertSystemSample(ctx context.Context, tx *sql.Tx, bucket time.Time, source string, sample domain.SystemSnapshot) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO system_samples(
		  bucket_start, source, cpu, load_1, memory_used, swap_used,
		  network_rx, network_tx, tcp_retrans, tcp_resets,
		  interface_drops, active_connections, proxy_service_ok,
		  collection_ok, missing_sources
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(bucket_start, source) DO UPDATE SET
		  cpu = excluded.cpu,
		  load_1 = excluded.load_1,
		  memory_used = excluded.memory_used,
		  swap_used = excluded.swap_used,
		  network_rx = system_samples.network_rx + excluded.network_rx,
		  network_tx = system_samples.network_tx + excluded.network_tx,
		  tcp_retrans = system_samples.tcp_retrans + excluded.tcp_retrans,
		  tcp_resets = system_samples.tcp_resets + excluded.tcp_resets,
		  interface_drops = system_samples.interface_drops + excluded.interface_drops,
		  active_connections = excluded.active_connections,
		  proxy_service_ok = MIN(system_samples.proxy_service_ok, excluded.proxy_service_ok),
		  collection_ok = MIN(system_samples.collection_ok, excluded.collection_ok),
		  missing_sources = excluded.missing_sources`,
		bucket.Unix(), source, sample.CPUPercent, sample.Load1,
		sample.MemoryUsedBytes, sample.SwapUsedBytes, sample.NetworkRXBytes,
		sample.NetworkTXBytes, sample.TCPRetransmits, sample.TCPResets,
		sample.InterfaceDrops, sample.ActiveConnection, boolInt(sample.ProxyServiceOK),
		boolInt(sample.CollectionOK), sample.MissingSources)
	if err != nil {
		return fmt.Errorf("insert system sample: %w", err)
	}
	return nil
}

// insertEvent persists only caller-supplied redacted text. Raw source errors
// remain confined to the local service journal.
// 中文：insertEvent 只保存调用方提供的脱敏文本；原始数据源错误仅保留在本地服务日志。
func insertEvent(ctx context.Context, tx *sql.Tx, occurredAt time.Time, credentialID *int64, severity, category, message string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO events(occurred_at, credential_id, severity, category, redacted_message)
		VALUES(?, ?, ?, ?, ?)`, occurredAt.Unix(), credentialID, severity, category, message)
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	return nil
}

// upsertCollectorState advances the cumulative baseline in the same transaction
// as its derived sample, preventing either side from committing alone.
// 中文：upsertCollectorState 与派生样本在同一事务中推进累计基线，
// 防止任一方单独提交造成状态不一致。
func upsertCollectorState(ctx context.Context, tx *sql.Tx, source string, credentialID int64, counters domain.Counters, observedAt time.Time, coreBootID string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO collector_state(
		  source, credential_id, last_upload_counter, last_download_counter,
		  last_success_at, core_boot_id
		) VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(source, credential_id) DO UPDATE SET
		  last_upload_counter = excluded.last_upload_counter,
		  last_download_counter = excluded.last_download_counter,
		  last_success_at = excluded.last_success_at,
		  core_boot_id = excluded.core_boot_id`,
		source, credentialID, counters.Upload, counters.Download, observedAt.Unix(), coreBootID)
	if err != nil {
		return fmt.Errorf("upsert collector state: %w", err)
	}
	return nil
}

// scanOptionalTime converts a nullable Unix timestamp without inventing a zero
// time for an empty table.
// 中文：scanOptionalTime 转换可空 Unix 时间戳，空表时不虚构零值时间。
func scanOptionalTime(row *sql.Row, target **time.Time) error {
	var value sql.NullInt64
	if err := row.Scan(&value); err != nil {
		return fmt.Errorf("scan optional time: %w", err)
	}
	if value.Valid {
		parsed := time.Unix(value.Int64, 0).UTC()
		*target = &parsed
	}
	return nil
}

// boolInt converts Go booleans to SQLite's integer representation.
// 中文：boolInt 将 Go 布尔值转换为 SQLite 整数表示。
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// secureDatabaseFiles reapplies owner-only permissions to the main database and
// transient WAL files after SQLite creates or rotates them.
// 中文：secureDatabaseFiles 在 SQLite 创建或轮换文件后，重新对主数据库及 WAL
// 临时文件应用仅所有者可读写权限。
func secureDatabaseFiles(path string) error {
	directoryInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("inspect database directory ownership: %w", err)
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Stat(candidate); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("inspect database file ownership: %w", err)
		}
		if err := matchDatabaseOwnership(candidate, directoryInfo); err != nil {
			return fmt.Errorf("restore database file ownership: %w", err)
		}
		if err := os.Chmod(candidate, 0o600); err != nil {
			return fmt.Errorf("restrict database file permissions: %w", err)
		}
	}
	return nil
}

// secureFiles skips read-only and in-memory stores, which have no owned files to
// harden.
// 中文：secureFiles 跳过只读和内存数据库，因为它们没有需要加固的自有文件。
func (s *Store) secureFiles() error {
	if s.readOnly || s.path == ":memory:" {
		return nil
	}
	return secureDatabaseFiles(s.path)
}
