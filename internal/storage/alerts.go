package storage

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strconv"
	"time"

	"vps-node-auditor/internal/config"
)

// EvaluateAlerts applies configured local-only thresholds and records each
// state transition once. It never disables credentials or sends notifications.
// 中文：EvaluateAlerts 应用仅限本地的阈值，并且每次状态转换只记录一次；它绝不
// 停用凭据，也不发送外部通知。
func (s *Store) EvaluateAlerts(ctx context.Context, cfg config.Config, now time.Time) error {
	location, err := maintenanceLocation(cfg)
	if err != nil {
		return err
	}
	cycle := billingCycleStart(now, location, cfg.Provider.CycleStartDay)
	usage, err := s.MonthUsage(ctx, cycle, now.UTC().Add(time.Second))
	if err != nil {
		return err
	}
	accounted := providerTraffic(usage.UserUploadBytes, usage.UserDownloadBytes, cfg.Provider.TrafficDirection)
	for _, threshold := range cfg.Alerts.QuotaPercentages {
		percent := float64(0)
		if cfg.Provider.MonthlyQuotaBytes > 0 {
			percent = float64(accounted) * 100 / float64(cfg.Provider.MonthlyQuotaBytes)
		}
		key := "quota:" + strconv.FormatInt(cycle.Unix(), 10) + ":" + strconv.FormatFloat(threshold, 'f', -1, 64)
		message := fmt.Sprintf("本地月度估算已达到 %.0f%% 额度阈值", threshold)
		if err := s.setAlert(ctx, key, percent >= threshold && cfg.Provider.MonthlyQuotaBytes > 0, percent, now, message); err != nil {
			return err
		}
	}
	if usage.VnStatAvailable {
		key := "traffic_difference:" + strconv.FormatInt(cycle.Unix(), 10)
		message := fmt.Sprintf("用户统计与网卡流量差异已达到 %.2f%% 阈值", cfg.Alerts.TrafficDifferencePct)
		if err := s.setAlert(ctx, key,
			usage.DifferencePercent >= cfg.Alerts.TrafficDifferencePct,
			usage.DifferencePercent, now, message); err != nil {
			return err
		}
	}
	consecutive, err := s.consecutiveFailures(ctx)
	if err != nil {
		return err
	}
	if err := s.setAlert(ctx, "consecutive_collection_failures",
		consecutive >= int64(cfg.Alerts.ConsecutiveFailures), float64(consecutive), now,
		"统计采集已达到配置的连续失败阈值"); err != nil {
		return err
	}
	if cfg.Alerts.UserHourlyBytes > 0 {
		start, _ := periodBounds(now.UTC(), "hour", location)
		if err := s.evaluateUserThreshold(ctx, start, now.UTC().Add(time.Second),
			cfg.Alerts.UserHourlyBytes, "user_hourly", now); err != nil {
			return err
		}
	}
	if cfg.Alerts.UserDailyBytes > 0 {
		start, _ := periodBounds(now.UTC(), "day", location)
		if err := s.evaluateUserThreshold(ctx, start, now.UTC().Add(time.Second),
			cfg.Alerts.UserDailyBytes, "user_daily", now); err != nil {
			return err
		}
	}
	if cfg.Alerts.HistoricalSpikeFactor > 0 {
		if err := s.evaluateHistoricalSpike(ctx, now.UTC(), cfg.Alerts.HistoricalSpikeFactor); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) setAlert(ctx context.Context, key string, active bool, value float64, now time.Time, message string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin alert transition: %w", err)
	}
	defer tx.Rollback()
	var priorActive int
	err = tx.QueryRowContext(ctx, "SELECT active FROM alert_state WHERE alert_key=?", key).Scan(&priorActive)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("read alert state: %w", err)
	}
	firstTriggered := any(nil)
	if active && (err == sql.ErrNoRows || priorActive == 0) {
		firstTriggered = now.UTC().Unix()
		if err := insertEvent(ctx, tx, now.UTC(), nil, "warning", "alert", message); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO alert_state(
		alert_key, active, first_triggered_at, last_evaluated_at, last_value, last_message)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(alert_key) DO UPDATE SET
		  active=excluded.active,
		  first_triggered_at=CASE
		    WHEN excluded.active=1 AND alert_state.active=0 THEN excluded.first_triggered_at
		    ELSE alert_state.first_triggered_at END,
		  last_evaluated_at=excluded.last_evaluated_at,
		  last_value=excluded.last_value,
		  last_message=excluded.last_message`,
		key, boolInt(active), firstTriggered, now.UTC().Unix(), value, message); err != nil {
		return fmt.Errorf("write alert state: %w", err)
	}
	return tx.Commit()
}

func (s *Store) consecutiveFailures(ctx context.Context) (int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT bucket_start, MIN(collection_ok)
		FROM traffic_samples GROUP BY bucket_start ORDER BY bucket_start DESC LIMIT 1000`)
	if err != nil {
		return 0, fmt.Errorf("query consecutive failures: %w", err)
	}
	defer rows.Close()
	var count int64
	for rows.Next() {
		var bucket int64
		var ok int
		if err := rows.Scan(&bucket, &ok); err != nil {
			return 0, fmt.Errorf("scan consecutive failure: %w", err)
		}
		if ok != 0 {
			break
		}
		count++
	}
	return count, rows.Err()
}

func (s *Store) evaluateUserThreshold(ctx context.Context, from, to time.Time, threshold int64, category string, now time.Time) error {
	rows, err := s.db.QueryContext(ctx, `SELECT u.id, u.display_name,
		COALESCE(SUM(t.upload_delta+t.download_delta),0)
		FROM users u JOIN credentials c ON c.user_id=u.id
		JOIN traffic_samples t ON t.credential_id=c.id
		WHERE t.collection_ok=1 AND t.bucket_start>=? AND t.bucket_start<?
		GROUP BY u.id, u.display_name`, from.Unix(), to.Unix())
	if err != nil {
		return fmt.Errorf("query user threshold: %w", err)
	}
	type item struct {
		userID int64
		name   string
		total  int64
	}
	var items []item
	for rows.Next() {
		var value item
		if err := rows.Scan(&value.userID, &value.name, &value.total); err != nil {
			rows.Close()
			return fmt.Errorf("scan user threshold: %w", err)
		}
		items = append(items, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, value := range items {
		key := category + ":" + strconv.FormatInt(from.Unix(), 10) + ":" + strconv.FormatInt(value.userID, 10)
		message := fmt.Sprintf("用户 %s 已超过配置的 %s 流量阈值", value.name, category)
		if err := s.setAlert(ctx, key, value.total >= threshold, float64(value.total), now, message); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) evaluateHistoricalSpike(ctx context.Context, now time.Time, factor float64) error {
	currentFrom := now.Add(-time.Hour)
	baselineFrom := currentFrom.Add(-24 * time.Hour)
	rows, err := s.db.QueryContext(ctx, `SELECT u.id, u.display_name,
		COALESCE(SUM(CASE WHEN t.bucket_start>=? THEN t.upload_delta+t.download_delta ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN t.bucket_start<? THEN t.upload_delta+t.download_delta ELSE 0 END),0)
		FROM users u JOIN credentials c ON c.user_id=u.id
		JOIN traffic_samples t ON t.credential_id=c.id
		WHERE t.collection_ok=1 AND t.bucket_start>=? AND t.bucket_start<?
		GROUP BY u.id, u.display_name`, currentFrom.Unix(), currentFrom.Unix(), baselineFrom.Unix(), now.Unix()+1)
	if err != nil {
		return fmt.Errorf("query historical spike: %w", err)
	}
	type spikeItem struct {
		userID, current, baseline int64
		name                      string
	}
	var items []spikeItem
	period := now.UTC().Truncate(time.Hour).Unix()
	for rows.Next() {
		var value spikeItem
		if err := rows.Scan(&value.userID, &value.name, &value.current, &value.baseline); err != nil {
			rows.Close()
			return fmt.Errorf("scan historical spike: %w", err)
		}
		items = append(items, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, value := range items {
		average := float64(value.baseline) / 24
		ratio := float64(0)
		if average > 0 {
			ratio = float64(value.current) / average
		}
		key := "historical_spike:" + strconv.FormatInt(period, 10) + ":" + strconv.FormatInt(value.userID, 10)
		message := fmt.Sprintf("用户 %s 的流量已超过配置的历史突增倍数", value.name)
		if err := s.setAlert(ctx, key, average > 0 && ratio >= factor, ratio, now, message); err != nil {
			return err
		}
	}
	return nil
}

func billingCycleStart(now time.Time, location *time.Location, day int) time.Time {
	local := now.In(location)
	start := time.Date(local.Year(), local.Month(), day, 0, 0, 0, 0, location)
	if local.Before(start) {
		start = start.AddDate(0, -1, 0)
	}
	return start.UTC()
}

func providerTraffic(upload, download int64, direction string) int64 {
	switch direction {
	case "upload":
		return upload
	case "download":
		return download
	case "max":
		return int64(math.Max(float64(upload), float64(download)))
	default:
		return upload + download
	}
}
