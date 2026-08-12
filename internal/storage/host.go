package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"vps-node-auditor/internal/domain"
)

// RecordHostSnapshot derives interval values from cumulative Linux counters
// and records vnStat independently from proxy-user accounting.
// 中文：RecordHostSnapshot 从 Linux 累计计数器推导区间值，并将 vnStat 作为
// 独立于代理用户记账的口径保存。
func (s *Store) RecordHostSnapshot(ctx context.Context, source string, raw domain.HostSnapshot) error {
	if raw.ObservedAt.IsZero() {
		return errors.New("host snapshot observed time is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin host snapshot: %w", err)
	}
	defer tx.Rollback()

	state, err := readSystemState(ctx, tx, source)
	if err != nil {
		return err
	}
	delta := func(name string, current int64, valid bool) int64 {
		if !valid {
			return 0
		}
		previous, found := state[name]
		value := safeCounterDelta(previous, found, current, raw.HostBootID)
		state[name] = systemState{Value: current, BootID: raw.HostBootID}
		return value
	}

	cpuTotal := delta("cpu_total", raw.CPUTotal, raw.CPUValid)
	cpuIdle := delta("cpu_idle", raw.CPUIdle, raw.CPUValid)
	cpuPercent := float64(0)
	if cpuTotal > 0 && cpuIdle >= 0 && cpuIdle <= cpuTotal {
		cpuPercent = float64(cpuTotal-cpuIdle) * 100 / float64(cpuTotal)
	}
	sample := domain.SystemSnapshot{
		CPUPercent:       cpuPercent,
		Load1:            raw.Load1,
		MemoryUsedBytes:  raw.MemoryUsedBytes,
		SwapUsedBytes:    raw.SwapUsedBytes,
		NetworkRXBytes:   delta("network_rx", raw.NetworkRXTotal, raw.NetworkValid),
		NetworkTXBytes:   delta("network_tx", raw.NetworkTXTotal, raw.NetworkValid),
		InterfaceDrops:   delta("interface_drops", raw.InterfaceDrops, raw.NetworkValid),
		TCPRetransmits:   delta("tcp_retrans", raw.TCPRetransTotal, raw.TCPValid),
		TCPResets:        delta("tcp_resets", raw.TCPResetsTotal, raw.TCPValid),
		ActiveConnection: raw.ActiveConnection,
		ProxyServiceOK:   raw.ProxyServiceOK,
		CollectionOK:     len(raw.MissingSources) == 0,
		MissingSources:   strings.Join(raw.MissingSources, ","),
	}
	if err := insertSystemSample(ctx, tx, raw.ObservedAt.UTC().Truncate(time.Minute), source, sample); err != nil {
		return err
	}
	for name, current := range state {
		if err := upsertSystemState(ctx, tx, source, name, current.Value,
			raw.ObservedAt, current.BootID); err != nil {
			return err
		}
	}
	if raw.VnStat != nil {
		var coverage any
		if !raw.VnStat.CoverageStart.IsZero() {
			coverage = raw.VnStat.CoverageStart.UTC().Unix()
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO vnstat_snapshots(
			  observed_at, interface_name, rx_total, tx_total, coverage_start, collection_ok)
			VALUES(?, ?, ?, ?, ?, ?)
			ON CONFLICT(observed_at, interface_name) DO UPDATE SET
			  rx_total = excluded.rx_total,
			  tx_total = excluded.tx_total,
			  coverage_start = excluded.coverage_start,
			  collection_ok = excluded.collection_ok`,
			raw.ObservedAt.UTC().Unix(), raw.VnStat.InterfaceName,
			raw.VnStat.RXTotal, raw.VnStat.TXTotal, coverage,
			boolInt(raw.VnStat.CollectionOK)); err != nil {
			return fmt.Errorf("insert vnStat snapshot: %w", err)
		}
	}
	if sample.MissingSources != "" {
		var previous sql.NullString
		_ = tx.QueryRowContext(ctx, `SELECT missing_sources FROM system_samples
			WHERE source = ? AND bucket_start < ? ORDER BY bucket_start DESC LIMIT 1`,
			source, raw.ObservedAt.UTC().Truncate(time.Minute).Unix()).Scan(&previous)
		if !previous.Valid || previous.String != sample.MissingSources {
			if err := insertEvent(ctx, tx, raw.ObservedAt.UTC(), nil, "warning",
				"system_evidence_gap", "system evidence unavailable: "+sample.MissingSources); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit host snapshot: %w", err)
	}
	return s.secureFiles()
}

type systemState struct {
	Value  int64
	BootID string
}

func readSystemState(ctx context.Context, tx *sql.Tx, source string) (map[string]systemState, error) {
	rows, err := tx.QueryContext(ctx, `SELECT metric_name, last_value, host_boot_id
		FROM system_collector_state WHERE source = ?`, source)
	if err != nil {
		return nil, fmt.Errorf("read system collector state: %w", err)
	}
	defer rows.Close()
	result := make(map[string]systemState)
	for rows.Next() {
		var name string
		var item systemState
		if err := rows.Scan(&name, &item.Value, &item.BootID); err != nil {
			return nil, fmt.Errorf("scan system collector state: %w", err)
		}
		result[name] = item
	}
	return result, rows.Err()
}

func safeCounterDelta(previous systemState, found bool, current int64, bootID string) int64 {
	if !found || current < previous.Value {
		return 0
	}
	if previous.BootID != "" && bootID != "" && previous.BootID != bootID {
		return 0
	}
	return current - previous.Value
}

func upsertSystemState(ctx context.Context, tx *sql.Tx, source, name string, value int64, observedAt time.Time, bootID string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO system_collector_state(
		source, metric_name, last_value, last_success_at, host_boot_id)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(source, metric_name) DO UPDATE SET
		  last_value = excluded.last_value,
		  last_success_at = excluded.last_success_at,
		  host_boot_id = excluded.host_boot_id`,
		source, name, value, observedAt.UTC().Unix(), bootID)
	if err != nil {
		return fmt.Errorf("update system collector state: %w", err)
	}
	return nil
}

// StatsMappings returns only enabled Stats API names and their internal UUIDs.
// The caller must not log or expose the returned map.
// 中文：StatsMappings 只返回启用的 Stats API 名称及其内部 UUID；调用方不得记录
// 或暴露该映射。
func (s *Store) StatsMappings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT stats_user, uuid FROM credentials
		WHERE enabled = 1 AND stats_user IS NOT NULL AND stats_user <> ''`)
	if err != nil {
		return nil, fmt.Errorf("query stats mappings: %w", err)
	}
	defer rows.Close()
	result := make(map[string]string)
	for rows.Next() {
		var statsUser, uuid string
		if err := rows.Scan(&statsUser, &uuid); err != nil {
			return nil, fmt.Errorf("scan stats mapping: %w", err)
		}
		result[statsUser] = uuid
	}
	return result, rows.Err()
}

// MonthUsage returns two explicitly separate accounting views over the same
// time range. vnStat coverage begins at the first stored snapshot in range.
// 中文：MonthUsage 返回同一时间范围内两个明确独立的计量口径；vnStat 覆盖从
// 范围内第一条已保存快照开始。
func (s *Store) MonthUsage(ctx context.Context, from, to time.Time) (domain.MonthUsage, error) {
	result := domain.MonthUsage{}
	if err := s.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(upload_delta), 0), COALESCE(SUM(download_delta), 0)
		FROM traffic_samples WHERE collection_ok = 1 AND bucket_start >= ? AND bucket_start < ?`,
		from.UTC().Unix(), to.UTC().Unix()).Scan(&result.UserUploadBytes, &result.UserDownloadBytes); err != nil {
		return domain.MonthUsage{}, fmt.Errorf("query monthly user traffic: %w", err)
	}
	result.UserTotalBytes = result.UserUploadBytes + result.UserDownloadBytes

	type point struct {
		At       int64
		RX       int64
		TX       int64
		Coverage sql.NullInt64
	}
	var first, last point
	firstErr := s.db.QueryRowContext(ctx, `SELECT observed_at, rx_total, tx_total, coverage_start
		FROM vnstat_snapshots WHERE collection_ok = 1 AND observed_at >= ? AND observed_at < ?
		ORDER BY observed_at LIMIT 1`, from.UTC().Unix(), to.UTC().Unix()).
		Scan(&first.At, &first.RX, &first.TX, &first.Coverage)
	if errors.Is(firstErr, sql.ErrNoRows) {
		return result, nil
	}
	if firstErr != nil {
		return domain.MonthUsage{}, fmt.Errorf("query first vnStat snapshot: %w", firstErr)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT observed_at, rx_total, tx_total, coverage_start
		FROM vnstat_snapshots WHERE collection_ok = 1 AND observed_at >= ? AND observed_at < ?
		ORDER BY observed_at DESC LIMIT 1`, from.UTC().Unix(), to.UTC().Unix()).
		Scan(&last.At, &last.RX, &last.TX, &last.Coverage); err != nil {
		return domain.MonthUsage{}, fmt.Errorf("query last vnStat snapshot: %w", err)
	}
	result.VnStatAvailable = true
	coverage := time.Unix(first.At, 0).UTC()
	if first.Coverage.Valid {
		created := time.Unix(first.Coverage.Int64, 0).UTC()
		if created.After(from.UTC()) && created.After(coverage) {
			coverage = created
		}
	}
	result.CoverageStart = &coverage
	if last.RX >= first.RX {
		result.InterfaceRXBytes = last.RX - first.RX
	}
	if last.TX >= first.TX {
		result.InterfaceTXBytes = last.TX - first.TX
	}
	result.InterfaceTotalBytes = result.InterfaceRXBytes + result.InterfaceTXBytes
	// Reconciliation must compare identical time coverage. The monthly user
	// total remains useful for quota estimation, but it may predate the first
	// locally stored vnStat snapshot and must not create a false alert.
	// 中文：对账必须使用相同覆盖时间。月度用户总量仍用于额度估算，但它
	// 可能早于首条本地 vnStat 快照，不应因此产生虚假差异告警。
	if err := s.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(upload_delta + download_delta), 0)
		FROM traffic_samples WHERE collection_ok = 1 AND bucket_start >= ? AND bucket_start < ?`,
		first.At, last.At).Scan(&result.CoveredUserBytes); err != nil {
		return domain.MonthUsage{}, fmt.Errorf("query covered user traffic: %w", err)
	}
	result.DifferenceBytes = result.InterfaceTotalBytes - result.CoveredUserBytes
	denominator := math.Max(float64(result.InterfaceTotalBytes), 1)
	result.DifferencePercent = math.Abs(float64(result.DifferenceBytes)) * 100 / denominator
	return result, nil
}

// IntegrityCheck runs SQLite's complete logical consistency check.
// 中文：IntegrityCheck 执行 SQLite 完整逻辑一致性检查。
func (s *Store) IntegrityCheck(ctx context.Context) error {
	var result string
	if err := s.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("run integrity check: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("integrity check returned %q", result)
	}
	return nil
}

// Checkpoint flushes and truncates the SQLite write-ahead log.
// 中文：Checkpoint 刷新并截断 SQLite 预写日志。
func (s *Store) Checkpoint(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("checkpoint database: %w", err)
	}
	return s.secureFiles()
}
