package storage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"vps-node-auditor/internal/config"
	"vps-node-auditor/internal/domain"
)

// MaintenanceResult reports deterministic database maintenance effects.
// 中文：MaintenanceResult 报告可核验的数据库维护结果。
type MaintenanceResult struct {
	TrafficRollups int64 `json:"traffic_rollups"`
	SystemRollups  int64 `json:"system_rollups"`
	TrafficDeleted int64 `json:"traffic_deleted"`
	SystemDeleted  int64 `json:"system_deleted"`
	EventsDeleted  int64 `json:"events_deleted"`
	RollupsDeleted int64 `json:"rollups_deleted"`
}

type trafficRollupKey struct {
	Kind       domain.TimelineBucket
	Start      int64
	Credential int64
}

type trafficRollupValue struct {
	Upload, Download                  int64
	Active, Samples, Gaps, ResetCount int64
}

type systemRollupKey struct {
	Kind  domain.TimelineBucket
	Start int64
}

type systemRollupValue struct {
	Samples, Gaps, ProxyFailures                                int64
	CPUSum, CPUMax, LoadSum, LoadMax                            float64
	MemoryMax, SwapMax, RX, TX, Retrans, Resets, Drops, ConnMax int64
}

// Maintain builds closed rollups, enforces retention, checkpoints WAL, and
// performs a full SQLite integrity check.
// 中文：Maintain 生成已闭合汇总、执行保留策略、检查点 WAL，并完成 SQLite
// 完整性检查。
func (s *Store) Maintain(ctx context.Context, cfg config.Config, now time.Time) (MaintenanceResult, error) {
	location, err := maintenanceLocation(cfg)
	if err != nil {
		return MaintenanceResult{}, err
	}
	now = now.UTC()
	traffic, err := s.buildTrafficRollups(ctx, now, location)
	if err != nil {
		return MaintenanceResult{}, err
	}
	systems, err := s.buildSystemRollups(ctx, now, location)
	if err != nil {
		return MaintenanceResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MaintenanceResult{}, fmt.Errorf("begin maintenance: %w", err)
	}
	defer tx.Rollback()
	result := MaintenanceResult{}
	for key, value := range traffic {
		if _, err := tx.ExecContext(ctx, `INSERT INTO traffic_rollups(
			bucket_kind, bucket_start, credential_id, upload_bytes, download_bytes,
			active_buckets, sample_buckets, gap_buckets, reset_count)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(bucket_kind, bucket_start, credential_id) DO UPDATE SET
			  upload_bytes=excluded.upload_bytes, download_bytes=excluded.download_bytes,
			  active_buckets=excluded.active_buckets, sample_buckets=excluded.sample_buckets,
			  gap_buckets=excluded.gap_buckets, reset_count=excluded.reset_count`,
			key.Kind, key.Start, key.Credential, value.Upload, value.Download,
			value.Active, value.Samples, value.Gaps, value.ResetCount); err != nil {
			return MaintenanceResult{}, fmt.Errorf("write traffic rollup: %w", err)
		}
		result.TrafficRollups++
	}
	for key, value := range systems {
		cpuAvg, loadAvg := float64(0), float64(0)
		if value.Samples > 0 {
			cpuAvg = value.CPUSum / float64(value.Samples)
			loadAvg = value.LoadSum / float64(value.Samples)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO system_rollups(
			bucket_kind, bucket_start, sample_count, gap_count, cpu_avg, cpu_max,
			load_1_avg, load_1_max, memory_used_max, swap_used_max, network_rx,
			network_tx, tcp_retrans, tcp_resets, interface_drops,
			active_connections_max, proxy_service_failures)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(bucket_kind, bucket_start) DO UPDATE SET
			  sample_count=excluded.sample_count, gap_count=excluded.gap_count,
			  cpu_avg=excluded.cpu_avg, cpu_max=excluded.cpu_max,
			  load_1_avg=excluded.load_1_avg, load_1_max=excluded.load_1_max,
			  memory_used_max=excluded.memory_used_max, swap_used_max=excluded.swap_used_max,
			  network_rx=excluded.network_rx, network_tx=excluded.network_tx,
			  tcp_retrans=excluded.tcp_retrans, tcp_resets=excluded.tcp_resets,
			  interface_drops=excluded.interface_drops,
			  active_connections_max=excluded.active_connections_max,
			  proxy_service_failures=excluded.proxy_service_failures`,
			key.Kind, key.Start, value.Samples, value.Gaps, cpuAvg, value.CPUMax,
			loadAvg, value.LoadMax, value.MemoryMax, value.SwapMax, value.RX, value.TX,
			value.Retrans, value.Resets, value.Drops, value.ConnMax, value.ProxyFailures); err != nil {
			return MaintenanceResult{}, fmt.Errorf("write system rollup: %w", err)
		}
		result.SystemRollups++
	}
	deleteCount := func(statement string, cutoff int64) (int64, error) {
		change, err := tx.ExecContext(ctx, statement, cutoff)
		if err != nil {
			return 0, err
		}
		return change.RowsAffected()
	}
	minuteCutoff := now.AddDate(0, 0, -cfg.Retention.MinuteDays).Unix()
	eventCutoff := now.AddDate(0, 0, -cfg.Retention.EventDays).Unix()
	rollupCutoff := now.AddDate(0, 0, -cfg.Retention.RollupDays).Unix()
	if result.TrafficDeleted, err = deleteCount("DELETE FROM traffic_samples WHERE bucket_start < ?", minuteCutoff); err != nil {
		return MaintenanceResult{}, fmt.Errorf("delete expired traffic: %w", err)
	}
	if result.SystemDeleted, err = deleteCount("DELETE FROM system_samples WHERE bucket_start < ?", minuteCutoff); err != nil {
		return MaintenanceResult{}, fmt.Errorf("delete expired system samples: %w", err)
	}
	if result.EventsDeleted, err = deleteCount("DELETE FROM events WHERE occurred_at < ?", eventCutoff); err != nil {
		return MaintenanceResult{}, fmt.Errorf("delete expired events: %w", err)
	}
	trafficDeleted, err := deleteCount("DELETE FROM traffic_rollups WHERE bucket_start < ?", rollupCutoff)
	if err != nil {
		return MaintenanceResult{}, fmt.Errorf("delete expired traffic rollups: %w", err)
	}
	systemDeleted, err := deleteCount("DELETE FROM system_rollups WHERE bucket_start < ?", rollupCutoff)
	if err != nil {
		return MaintenanceResult{}, fmt.Errorf("delete expired system rollups: %w", err)
	}
	result.RollupsDeleted = trafficDeleted + systemDeleted
	if _, err := tx.ExecContext(ctx, "DELETE FROM vnstat_snapshots WHERE observed_at < ?", rollupCutoff); err != nil {
		return MaintenanceResult{}, fmt.Errorf("delete expired vnStat snapshots: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return MaintenanceResult{}, fmt.Errorf("commit maintenance: %w", err)
	}
	if err := s.Checkpoint(ctx); err != nil {
		return MaintenanceResult{}, err
	}
	if err := s.IntegrityCheck(ctx); err != nil {
		return MaintenanceResult{}, err
	}
	return result, s.secureFiles()
}

func (s *Store) buildTrafficRollups(ctx context.Context, now time.Time, location *time.Location) (map[trafficRollupKey]trafficRollupValue, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT bucket_start, credential_id, upload_delta,
		download_delta, active, collection_ok, reset_detected FROM traffic_samples`)
	if err != nil {
		return nil, fmt.Errorf("read traffic samples for rollup: %w", err)
	}
	defer rows.Close()
	result := make(map[trafficRollupKey]trafficRollupValue)
	for rows.Next() {
		var at, credential, upload, download int64
		var active, ok, reset int
		if err := rows.Scan(&at, &credential, &upload, &download, &active, &ok, &reset); err != nil {
			return nil, fmt.Errorf("scan traffic rollup input: %w", err)
		}
		observed := time.Unix(at, 0).UTC()
		for _, kind := range []domain.TimelineBucket{domain.BucketHour, domain.BucketDay, domain.BucketMonth} {
			start, end := periodBounds(observed, kind, location)
			if end.After(now) {
				continue
			}
			key := trafficRollupKey{Kind: kind, Start: start.Unix(), Credential: credential}
			value := result[key]
			if ok != 0 {
				value.Upload += upload
				value.Download += download
			} else {
				value.Gaps++
			}
			value.Active += int64(active)
			value.Samples++
			value.ResetCount += int64(reset)
			result[key] = value
		}
	}
	return result, rows.Err()
}

func (s *Store) buildSystemRollups(ctx context.Context, now time.Time, location *time.Location) (map[systemRollupKey]systemRollupValue, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT bucket_start, cpu, load_1, memory_used,
		swap_used, network_rx, network_tx, tcp_retrans, tcp_resets, interface_drops,
		active_connections, proxy_service_ok, collection_ok FROM system_samples`)
	if err != nil {
		return nil, fmt.Errorf("read system samples for rollup: %w", err)
	}
	defer rows.Close()
	result := make(map[systemRollupKey]systemRollupValue)
	for rows.Next() {
		var at, memory, swap, rx, tx, retrans, resets, drops, connections int64
		var cpu, load float64
		var proxyOK, collectionOK int
		if err := rows.Scan(&at, &cpu, &load, &memory, &swap, &rx, &tx, &retrans,
			&resets, &drops, &connections, &proxyOK, &collectionOK); err != nil {
			return nil, fmt.Errorf("scan system rollup input: %w", err)
		}
		observed := time.Unix(at, 0).UTC()
		for _, kind := range []domain.TimelineBucket{domain.BucketHour, domain.BucketDay, domain.BucketMonth} {
			start, end := periodBounds(observed, kind, location)
			if end.After(now) {
				continue
			}
			key := systemRollupKey{Kind: kind, Start: start.Unix()}
			value := result[key]
			value.Samples++
			if collectionOK == 0 {
				value.Gaps++
			}
			if proxyOK == 0 {
				value.ProxyFailures++
			}
			value.CPUSum += cpu
			value.LoadSum += load
			value.CPUMax = math.Max(value.CPUMax, cpu)
			value.LoadMax = math.Max(value.LoadMax, load)
			value.MemoryMax = max(value.MemoryMax, memory)
			value.SwapMax = max(value.SwapMax, swap)
			value.RX += rx
			value.TX += tx
			value.Retrans += retrans
			value.Resets += resets
			value.Drops += drops
			value.ConnMax = max(value.ConnMax, connections)
			result[key] = value
		}
	}
	return result, rows.Err()
}

func periodBounds(at time.Time, kind domain.TimelineBucket, location *time.Location) (time.Time, time.Time) {
	local := at.In(location)
	var start, end time.Time
	switch kind {
	case domain.BucketMonth:
		start = time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, location)
		end = start.AddDate(0, 1, 0)
	case domain.BucketDay:
		start = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
		end = start.AddDate(0, 0, 1)
	default:
		start = time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), 0, 0, 0, location)
		end = start.Add(time.Hour)
	}
	return start.UTC(), end.UTC()
}

func maintenanceLocation(cfg config.Config) (*time.Location, error) {
	name := cfg.Provider.BillingTimezone
	if name == "" {
		name = cfg.Display.Timezone
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("load maintenance timezone: %w", err)
	}
	return location, nil
}

// TimelineBucket returns minute data or closed rollups plus uncovered raw
// samples at the requested stable granularity.
// 中文：TimelineBucket 返回分钟数据，或已闭合汇总加尚未覆盖的原始样本。
func (s *Store) TimelineBucket(ctx context.Context, name string, from, to time.Time, kind domain.TimelineBucket, location *time.Location) ([]domain.TimelineEntry, error) {
	if kind == domain.BucketMinute {
		return s.Timeline(ctx, name, from, to)
	}
	if kind != domain.BucketHour && kind != domain.BucketDay && kind != domain.BucketMonth {
		return nil, fmt.Errorf("unsupported timeline bucket %q", kind)
	}
	type credentialBucket struct{ Credential, Start int64 }
	covered := make(map[credentialBucket]struct{})
	result := make(map[int64]domain.TimelineEntry)
	qualitySeen := make(map[int64]bool)
	rows, err := s.db.QueryContext(ctx, `SELECT r.bucket_start, r.credential_id,
		r.upload_bytes, r.download_bytes, r.active_buckets, r.gap_buckets, r.reset_count
		FROM traffic_rollups r JOIN credentials c ON c.id=r.credential_id
		JOIN users u ON u.id=c.user_id
		WHERE r.bucket_kind=? AND r.bucket_start >= ? AND r.bucket_start < ?
		  AND (u.display_name=? OR c.display_name=?)`,
		kind, from.UTC().Unix(), to.UTC().Unix(), name, name)
	if err != nil {
		return nil, fmt.Errorf("query timeline rollups: %w", err)
	}
	for rows.Next() {
		var start, credential, upload, download, active, gaps, resets int64
		if err := rows.Scan(&start, &credential, &upload, &download, &active, &gaps, &resets); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan timeline rollup: %w", err)
		}
		covered[credentialBucket{credential, start}] = struct{}{}
		item := result[start]
		item.BucketStart = time.Unix(start, 0).UTC()
		item.UploadBytes += upload
		item.DownloadBytes += download
		item.Active = item.Active || active > 0
		if !qualitySeen[start] {
			item.CollectionOK = gaps == 0
			qualitySeen[start] = true
		} else {
			item.CollectionOK = item.CollectionOK && gaps == 0
		}
		item.ResetDetected = item.ResetDetected || resets > 0
		item.EvidenceSource = "rollup:" + string(kind)
		result[start] = item
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	raw, err := s.db.QueryContext(ctx, `SELECT t.bucket_start, t.credential_id,
		t.upload_delta, t.download_delta, t.active, t.collection_ok, t.reset_detected
		FROM traffic_samples t JOIN credentials c ON c.id=t.credential_id
		JOIN users u ON u.id=c.user_id
		WHERE t.bucket_start >= ? AND t.bucket_start < ?
		  AND (u.display_name=? OR c.display_name=?)`,
		from.UTC().Unix(), to.UTC().Unix(), name, name)
	if err != nil {
		return nil, fmt.Errorf("query uncovered timeline samples: %w", err)
	}
	defer raw.Close()
	for raw.Next() {
		var at, credential, upload, download int64
		var active, ok, reset int
		if err := raw.Scan(&at, &credential, &upload, &download, &active, &ok, &reset); err != nil {
			return nil, fmt.Errorf("scan uncovered timeline sample: %w", err)
		}
		start, _ := periodBounds(time.Unix(at, 0).UTC(), kind, location)
		key := credentialBucket{credential, start.Unix()}
		if _, exists := covered[key]; exists {
			continue
		}
		item := result[start.Unix()]
		item.BucketStart = start
		if ok != 0 {
			item.UploadBytes += upload
			item.DownloadBytes += download
		}
		item.Active = item.Active || active != 0
		if !qualitySeen[start.Unix()] {
			item.CollectionOK = ok != 0
			qualitySeen[start.Unix()] = true
		} else {
			item.CollectionOK = item.CollectionOK && ok != 0
		}
		item.ResetDetected = item.ResetDetected || reset != 0
		item.EvidenceSource = "raw:" + string(kind)
		result[start.Unix()] = item
	}
	items := make([]domain.TimelineEntry, 0, len(result))
	for _, item := range result {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].BucketStart.Before(items[j].BucketStart) })
	return items, raw.Err()
}

// Backup creates a transactionally consistent, private SQLite copy using
// VACUUM INTO. The destination must not already exist.
// 中文：Backup 使用 VACUUM INTO 创建事务一致、权限私有的 SQLite 副本；目标文件
// 不得预先存在。
func (s *Store) Backup(ctx context.Context, destination string) error {
	if destination == "" {
		return errors.New("backup destination is required")
	}
	if _, err := os.Stat(destination); err == nil {
		return errors.New("backup destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect backup destination: %w", err)
	}
	directory := filepath.Dir(destination)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("restrict backup directory: %w", err)
	}
	literal := strings.ReplaceAll(filepath.ToSlash(destination), "'", "''")
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO '"+literal+"'"); err != nil {
		return fmt.Errorf("create consistent backup: %w", err)
	}
	if err := os.Chmod(destination, 0o600); err != nil {
		return fmt.Errorf("restrict backup file: %w", err)
	}
	return nil
}
