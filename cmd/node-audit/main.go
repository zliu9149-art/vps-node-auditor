// Command node-audit provides read-only operator queries over the local audit
// database. Human-readable output is Chinese; JSON output remains stable for
// automation.
// 中文：node-audit 命令以只读方式查询本地审计数据库；人工阅读使用中文，
// JSON 字段保持稳定，供自动化脚本使用。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"vps-node-auditor/internal/config"
	"vps-node-auditor/internal/domain"
	"vps-node-auditor/internal/source"
	"vps-node-auditor/internal/storage"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "node-audit: %v\n", err)
		os.Exit(1)
	}
}

// run loads the shared configuration, opens SQLite in enforced read-only mode,
// and dispatches exactly one query command.
// 中文：run 加载共享配置，以强制只读模式打开 SQLite，并且每次只执行一个查询命令。
func run(ctx context.Context, args []string, output io.Writer) error {
	configPath, command, commandArgs, err := splitGlobalArgs(args)
	if err != nil {
		return err
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	location, err := cfg.DisplayLocation()
	if err != nil {
		return err
	}
	store, err := storage.Open(cfg.Database.Path, true)
	if err != nil {
		return err
	}
	defer store.Close()

	switch command {
	case "top":
		return runTop(ctx, store, commandArgs, output)
	case "user":
		return runUser(ctx, store, commandArgs, output, location)
	case "incident":
		return runIncident(ctx, store, commandArgs, output, location)
	case "month":
		return runMonth(ctx, store, cfg, commandArgs, output)
	case "health":
		return runHealth(ctx, store, cfg, commandArgs, output, location)
	default:
		return fmt.Errorf("unknown command %q; use top, user, incident, month, or health", command)
	}
}

// splitGlobalArgs extracts options that must appear before the subcommand while
// leaving subcommand-specific arguments untouched for their own FlagSet.
// 中文：splitGlobalArgs 提取必须位于子命令之前的全局参数，并将其余参数原样交给子命令解析。
func splitGlobalArgs(args []string) (string, string, []string, error) {
	configPath := "config.json"
	for len(args) > 0 {
		switch {
		case args[0] == "--config":
			if len(args) < 2 {
				return "", "", nil, errors.New("--config requires a path")
			}
			configPath = args[1]
			args = args[2:]
		case strings.HasPrefix(args[0], "--config="):
			configPath = strings.TrimPrefix(args[0], "--config=")
			args = args[1:]
		default:
			if args[0] == "-h" || args[0] == "--help" {
				return "", "", nil, errors.New("usage: node-audit [--config path] <top|user|incident|month|health> [options]")
			}
			return configPath, args[0], args[1:], nil
		}
	}
	return "", "", nil, errors.New("a command is required")
}

// runTop prints traffic totals aggregated by registered user or, when
// requested, by independently auditable credential.
// 中文：runTop 默认按注册用户汇总流量，也可按独立凭据输出可审计排行。
func runTop(ctx context.Context, store *storage.Store, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("top", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	since := flags.String("since", "24h", "lookback duration")
	limit := flags.Int("limit", 10, "maximum rows")
	byCredential := flags.Bool("by-credential", false, "rank individual credentials instead of registered users")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	lookback, err := parseLookback(*since)
	if err != nil {
		return err
	}
	if *limit < 1 {
		return errors.New("--limit must be positive")
	}
	now := time.Now().UTC()
	var entries []domain.TopEntry
	if *byCredential {
		entries, err = store.CredentialTop(ctx, now.Add(-lookback), now)
	} else {
		entries, err = store.Top(ctx, now.Add(-lookback), now)
	}
	if err != nil {
		return err
	}
	if len(entries) > *limit {
		entries = entries[:*limit]
	}
	if *jsonOutput {
		return writeJSON(output, entries)
	}
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "用户\t凭据\t上行\t下行\t总量\t占比")
	for _, entry := range entries {
		credentialName := entry.CredentialName
		if credentialName == "" {
			credentialName = "全部"
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%.2f%%\n",
			entry.UserName, credentialName, formatBytes(entry.UploadBytes),
			formatBytes(entry.DownloadBytes), formatBytes(entry.TotalBytes), entry.SharePercent)
	}
	return writer.Flush()
}

// runUser prints minute buckets matching either a registered user display name
// or one credential display name.
// 中文：runUser 按注册用户名或凭据显示名查询并输出分钟级流量记录。
func runUser(ctx context.Context, store *storage.Store, args []string, output io.Writer, location *time.Location) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return errors.New("user requires a registered user or credential name")
	}
	name := args[0]
	flags := flag.NewFlagSet("user", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	since := flags.String("since", "7d", "lookback duration")
	bucketText := flags.String("bucket", "auto", "auto, minute, hour, day, or month")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	lookback, err := parseLookback(*since)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	bucket, err := selectBucket(*bucketText, lookback)
	if err != nil {
		return err
	}
	entries, err := store.TimelineBucket(ctx, name, now.Add(-lookback), now, bucket, location)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(output, entries)
	}
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fmt.Fprintf(writer, "粒度：%s\n", bucket)
	fmt.Fprintln(writer, "时间\t上行\t下行\t活动\t采集\t重置\t来源")
	for _, entry := range entries {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			entry.BucketStart.In(location).Format("2006-01-02 15:04"),
			formatBytes(entry.UploadBytes), formatBytes(entry.DownloadBytes),
			yesNo(entry.Active), okGap(entry.CollectionOK), yesNo(entry.ResetDetected), entry.EvidenceSource)
	}
	return writer.Flush()
}

// runIncident correlates redacted events, host samples, and optional user
// activity within an explicit operator-supplied time range.
// 中文：runIncident 在指定时间范围内关联脱敏事件、主机样本和可选的用户活动记录。
func runIncident(ctx context.Context, store *storage.Store, args []string, output io.Writer, location *time.Location) error {
	flags := flag.NewFlagSet("incident", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	fromText := flags.String("from", "", "start time")
	toText := flags.String("to", "", "end time")
	user := flags.String("user", "", "optional registered user or credential name")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *fromText == "" || *toText == "" {
		return errors.New("incident requires --from and --to")
	}
	from, err := parseTime(*fromText, location)
	if err != nil {
		return fmt.Errorf("parse --from: %w", err)
	}
	to, err := parseTime(*toText, location)
	if err != nil {
		return fmt.Errorf("parse --to: %w", err)
	}
	if !to.After(from) {
		return errors.New("--to must be later than --from")
	}
	events, err := store.Events(ctx, from, to)
	if err != nil {
		return err
	}
	systems, err := store.SystemSamples(ctx, from, to)
	if err != nil {
		return err
	}
	var timeline []domain.TimelineEntry
	if *user != "" {
		timeline, err = store.Timeline(ctx, *user, from, to)
		if err != nil {
			return err
		}
	}
	otherUsers, err := store.Top(ctx, from, to)
	if err != nil {
		return err
	}
	if *user != "" {
		filtered := otherUsers[:0]
		for _, entry := range otherUsers {
			if entry.UserName != *user {
				filtered = append(filtered, entry)
			}
		}
		otherUsers = filtered
	}
	var stoppedAt, recoveredAt *time.Time
	var gapBuckets int64
	wasActive := false
	for i := range timeline {
		if !timeline[i].CollectionOK {
			gapBuckets++
		}
		if wasActive && !timeline[i].Active && stoppedAt == nil {
			value := timeline[i].BucketStart
			stoppedAt = &value
		}
		if stoppedAt != nil && timeline[i].Active {
			value := timeline[i].BucketStart
			recoveredAt = &value
			break
		}
		wasActive = timeline[i].Active
	}
	report := struct {
		From        time.Time              `json:"from"`
		To          time.Time              `json:"to"`
		User        string                 `json:"user,omitempty"`
		Timeline    []domain.TimelineEntry `json:"timeline,omitempty"`
		OtherUsers  []domain.TopEntry      `json:"other_users"`
		System      []domain.SystemSample  `json:"system"`
		Events      []domain.Event         `json:"events"`
		StoppedAt   *time.Time             `json:"stopped_at,omitempty"`
		RecoveredAt *time.Time             `json:"recovered_at,omitempty"`
		GapBuckets  int64                  `json:"gap_buckets"`
	}{from, to, *user, timeline, otherUsers, systems, events, stoppedAt, recoveredAt, gapBuckets}
	if *jsonOutput {
		return writeJSON(output, report)
	}
	fmt.Fprintf(output, "时间范围：%s 至 %s\n", from.In(location).Format(time.RFC3339), to.In(location).Format(time.RFC3339))
	fmt.Fprintf(output, "用户活动桶：%d，其他用户：%d，系统样本：%d，采集缺口：%d，事件：%d\n",
		len(timeline), len(otherUsers), len(systems), gapBuckets, len(events))
	if stoppedAt != nil {
		fmt.Fprintf(output, "活动停止：%s\n", stoppedAt.In(location).Format(time.RFC3339))
	}
	if recoveredAt != nil {
		fmt.Fprintf(output, "活动恢复：%s\n", recoveredAt.In(location).Format(time.RFC3339))
	}
	for _, entry := range otherUsers {
		fmt.Fprintf(output, "同期其他用户 %s：%s\n", entry.UserName, formatBytes(entry.TotalBytes))
	}
	for _, sample := range systems {
		if !sample.CollectionOK || sample.TCPRetransmits > 0 || sample.TCPResets > 0 {
			fmt.Fprintf(output, "%s 系统：CPU %.1f%%，TCP重传 %d，reset %d，采集 %s（%s）\n",
				sample.BucketStart.In(location).Format("2006-01-02 15:04"), sample.CPUPercent,
				sample.TCPRetransmits, sample.TCPResets, okGap(sample.CollectionOK), sample.MissingSources)
		}
	}
	for _, event := range events {
		fmt.Fprintf(output, "%s [%s/%s] %s\n", event.OccurredAt.In(location).Format("2006-01-02 15:04"), event.Severity, event.Category, event.Message)
	}
	return nil
}

// runMonth estimates provider-accounted traffic for the configured billing
// cycle. The provider control panel remains authoritative.
// 中文：runMonth 根据配置的计费周期估算服务商口径流量，最终结果仍以服务商控制台为准。
func runMonth(ctx context.Context, store *storage.Store, cfg config.Config, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("month", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	location, err := billingLocation(cfg)
	if err != nil {
		return err
	}
	now := time.Now()
	from := cycleStart(now, location, cfg.Provider.CycleStartDay)
	usage, err := store.MonthUsage(ctx, from, now.UTC().Add(time.Second))
	if err != nil {
		return err
	}
	accounted := providerBytes(usage.UserUploadBytes, usage.UserDownloadBytes, cfg.Provider.TrafficDirection)
	percent := float64(0)
	if cfg.Provider.MonthlyQuotaBytes > 0 {
		percent = float64(accounted) * 100 / float64(cfg.Provider.MonthlyQuotaBytes)
	}
	report := struct {
		CycleStart       time.Time  `json:"cycle_start"`
		UploadBytes      int64      `json:"upload_bytes"`
		DownloadBytes    int64      `json:"download_bytes"`
		UserTotalBytes   int64      `json:"user_total_bytes"`
		CoveredUserBytes int64      `json:"covered_user_total_bytes"`
		InterfaceBytes   int64      `json:"interface_total_bytes"`
		DifferenceBytes  int64      `json:"difference_bytes"`
		DifferencePct    float64    `json:"difference_percent"`
		CoverageStart    *time.Time `json:"coverage_start,omitempty"`
		VnStatAvailable  bool       `json:"vnstat_available"`
		EstimatedBilling int64      `json:"estimated_billing_bytes"`
		QuotaBytes       int64      `json:"quota_bytes"`
		QuotaPercent     float64    `json:"quota_percent"`
		EstimateOnly     bool       `json:"estimate_only"`
	}{from, usage.UserUploadBytes, usage.UserDownloadBytes, usage.UserTotalBytes, usage.CoveredUserBytes,
		usage.InterfaceTotalBytes, usage.DifferenceBytes, usage.DifferencePercent,
		usage.CoverageStart, usage.VnStatAvailable, accounted,
		cfg.Provider.MonthlyQuotaBytes, percent, true}
	if *jsonOutput {
		return writeJSON(output, report)
	}
	fmt.Fprintf(output, "计费周期起点：%s\n", from.In(location).Format(time.RFC3339))
	fmt.Fprintf(output, "用户上行合计：%s\n用户下行合计：%s\n用户双向总量：%s\n",
		formatBytes(usage.UserUploadBytes), formatBytes(usage.UserDownloadBytes), formatBytes(usage.UserTotalBytes))
	if usage.VnStatAvailable {
		fmt.Fprintf(output, "对账覆盖内用户总量：%s\n服务器网卡总量：%s\n同覆盖区间差异：%s（%.2f%%）\n",
			formatBytes(usage.CoveredUserBytes), formatBytes(usage.InterfaceTotalBytes),
			formatBytes(usage.DifferenceBytes), usage.DifferencePercent)
		fmt.Fprintf(output, "vnStat 对账覆盖起点：%s\n", formatOptionalTime(usage.CoverageStart, location))
	} else {
		fmt.Fprintln(output, "服务器网卡总量：暂无可用 vnStat 覆盖")
	}
	fmt.Fprintf(output, "按 %s 口径估算：%s（额度 %.2f%%）\n", cfg.Provider.TrafficDirection, formatBytes(accounted), percent)
	fmt.Fprintln(output, "注意：这是本机估算值，服务商控制台仍是最终依据。")
	return nil
}

// runHealth reports database readability, sample freshness, enabled credential
// count, and collection failures observed during the previous 24 hours.
// 中文：runHealth 报告数据库可读性、样本新鲜度、启用凭据数及最近 24 小时采集失败数。
func runHealth(ctx context.Context, store *storage.Store, cfg config.Config, args []string, output io.Writer, location *time.Location) error {
	flags := flag.NewFlagSet("health", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	health, err := store.Health(ctx, time.Now())
	if err != nil {
		return err
	}
	if err := store.IntegrityCheck(ctx); err != nil {
		health.DatabaseOK = false
	}
	if cfg.Collector.Source == "sing-box-v2ray" {
		health.StatsAPIOK = source.ProbeV2RayAPI(ctx, cfg.Collector.V2RayAPI.Address, cfg.CollectorTimeout()) == nil
		commandCtx, cancel := context.WithTimeout(ctx, cfg.HostCommandTimeout())
		_, serviceErr := exec.CommandContext(commandCtx, "systemctl", "is-active", "--quiet", cfg.Collector.V2RayAPI.ProcessName).Output()
		cancel()
		health.ProxyServiceOK = serviceErr == nil
	}
	if !cfg.Host.Disabled {
		commandCtx, cancel := context.WithTimeout(ctx, cfg.HostCommandTimeout())
		_, vnstatErr := exec.CommandContext(commandCtx, "vnstat", "--json", "f", "1", "--iface", cfg.Host.Interface).Output()
		cancel()
		health.VnStatOK = vnstatErr == nil
	}
	if *jsonOutput {
		return writeJSON(output, health)
	}
	fmt.Fprintf(output, "数据库：%s\n启用凭据：%d\n最近 24 小时采集失败：%d\n",
		okGap(health.DatabaseOK), health.EnabledCredentials, health.RecentFailures)
	fmt.Fprintf(output, "最近流量样本：%s\n", formatOptionalTime(health.LastTrafficSampleAt, location))
	fmt.Fprintf(output, "最近系统样本：%s\n", formatOptionalTime(health.LastSystemSampleAt, location))
	fmt.Fprintf(output, "流量新鲜度：%s\n系统新鲜度：%s\nStats API：%s\nvnStat：%s\nsing-box：%s\n",
		okGap(health.TrafficFresh), okGap(health.SystemFresh), okGap(health.StatsAPIOK),
		okGap(health.VnStatOK), okGap(health.ProxyServiceOK))
	fmt.Fprintf(output, "连续失败：%d\n系统采集：%s", health.ConsecutiveFailures, okGap(health.SystemCollectionOK))
	if health.SystemMissing != "" {
		fmt.Fprintf(output, "（缺失：%s）", health.SystemMissing)
	}
	fmt.Fprintln(output)
	return nil
}

// selectBucket validates an explicit bucket or chooses a practical default
// from the requested time range.
// 中文：selectBucket 校验显式粒度，或根据查询时间范围选择实用默认值。
func selectBucket(value string, lookback time.Duration) (domain.TimelineBucket, error) {
	if value == "auto" {
		switch {
		case lookback <= 48*time.Hour:
			return domain.BucketMinute, nil
		case lookback <= 14*24*time.Hour:
			return domain.BucketHour, nil
		case lookback <= 180*24*time.Hour:
			return domain.BucketDay, nil
		default:
			return domain.BucketMonth, nil
		}
	}
	bucket := domain.TimelineBucket(value)
	switch bucket {
	case domain.BucketMinute, domain.BucketHour, domain.BucketDay, domain.BucketMonth:
		return bucket, nil
	default:
		return "", errors.New("--bucket must be auto, minute, hour, day, or month")
	}
}

// parseLookback accepts Go duration syntax plus a positive whole-day suffix.
// 中文：parseLookback 接受 Go 时间长度格式，也接受以 d 表示的正整数天数。
func parseLookback(value string) (time.Duration, error) {
	if strings.HasSuffix(value, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(value, "d"))
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("invalid day duration %q", value)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("invalid duration %q", value)
	}
	return duration, nil
}

// parseTime accepts an unambiguous RFC3339 timestamp or interprets a compact
// operator-entered timestamp in the configured display timezone.
// 中文：parseTime 接受 RFC3339 时间；简写时间则按配置的显示时区解释。
func parseTime(value string, location *time.Location) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC(), nil
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04", value, location)
	if err != nil {
		return time.Time{}, errors.New("expected RFC3339 or YYYY-MM-DD HH:MM")
	}
	return parsed.UTC(), nil
}

// billingLocation resolves the provider-specific timezone, falling back to the
// display timezone only when no billing timezone is configured.
// 中文：billingLocation 优先使用服务商计费时区，未配置时才回退到显示时区。
func billingLocation(cfg config.Config) (*time.Location, error) {
	if cfg.Provider.BillingTimezone == "" {
		return cfg.DisplayLocation()
	}
	return time.LoadLocation(cfg.Provider.BillingTimezone)
}

// cycleStart returns the most recent configured billing-day boundary in UTC.
// 中文：cycleStart 计算最近一次计费周期起点，并统一转换为 UTC。
func cycleStart(now time.Time, location *time.Location, day int) time.Time {
	local := now.In(location)
	start := time.Date(local.Year(), local.Month(), day, 0, 0, 0, 0, location)
	if local.Before(start) {
		start = start.AddDate(0, -1, 0)
	}
	return start.UTC()
}

// providerBytes applies the provider's configured traffic-accounting rule.
// 中文：providerBytes 按服务商配置的上行、下行、双向或最大值口径计算流量。
func providerBytes(upload, download int64, direction string) int64 {
	switch direction {
	case "upload":
		return upload
	case "download":
		return download
	case "max":
		if upload > download {
			return upload
		}
		return download
	default:
		return upload + download
	}
}

// writeJSON emits indented JSON without translating stable field names.
// 中文：writeJSON 输出缩进 JSON，并保留稳定的英文字段名。
func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

// formatBytes renders byte counts with IEC binary units.
// 中文：formatBytes 使用 IEC 二进制单位格式化字节数。
func formatBytes(value int64) string {
	sign := ""
	if value < 0 {
		sign = "-"
		value = -value
	}
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%s%d B", sign, value)
	}
	divisor, exponent := int64(unit), 0
	for quotient := value / unit; quotient >= unit && exponent < len("KMGTPE")-1; quotient /= unit {
		divisor *= unit
		exponent++
	}
	return fmt.Sprintf("%s%.2f %ciB", sign, float64(value)/float64(divisor), "KMGTPE"[exponent])
}

// yesNo renders a boolean for the Chinese human-readable interface.
// 中文：yesNo 将布尔值转换为中文“是”或“否”。
func yesNo(value bool) string {
	if value {
		return "是"
	}
	return "否"
}

// okGap distinguishes a valid sample from an explicitly recorded evidence gap.
// 中文：okGap 区分有效样本和明确记录的证据缺口。
func okGap(value bool) string {
	if value {
		return "正常"
	}
	return "缺口"
}

// formatOptionalTime renders missing evidence explicitly rather than inventing
// a zero timestamp.
// 中文：formatOptionalTime 对缺失时间明确显示“无”，不伪造零值时间。
func formatOptionalTime(value *time.Time, location *time.Location) string {
	if value == nil {
		return "无"
	}
	return value.In(location).Format(time.RFC3339)
}
