// Command node-audit-maintenance performs owner-authorized local database
// maintenance and backups. It does not listen on a network socket.
// 中文：node-audit-maintenance 执行所有者授权的本地数据库维护和备份，且不监听
// 任何网络端口。
package main

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"vps-node-auditor/internal/config"
	"vps-node-auditor/internal/lockfile"
	"vps-node-auditor/internal/storage"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "node-audit-maintenance: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	configPath, command, commandArgs, err := splitArgs(args)
	if err != nil {
		return err
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if os.Getenv("VNA_OPERATIONS_LOCK_HELD") != "1" {
		lock, busy, err := lockfile.TryAcquire(cfg.Collector.ConfigLockPath)
		if err != nil {
			return fmt.Errorf("acquire operations lock: %w", err)
		}
		if busy {
			return errors.New("another operation holds the global lock")
		}
		defer lock.Close()
	}
	databasePath := cfg.Database.Path
	readOnly := false
	if command == "integrity" {
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		database := flags.String("database", databasePath, "database file to validate")
		if err := flags.Parse(commandArgs); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("integrity accepts only --database")
		}
		databasePath = *database
		readOnly = true
	}
	store, err := storage.Open(databasePath, readOnly)
	if err != nil {
		return err
	}
	defer store.Close()
	if !readOnly {
		if err := store.Migrate(ctx); err != nil {
			return err
		}
	}
	if command == "integrity" {
		if err := store.IntegrityCheck(ctx); err != nil {
			return err
		}
		fmt.Println("database integrity check: ok")
		return nil
	}
	switch command {
	case "maintain":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		jsonOutput := flags.Bool("json", false, "emit JSON")
		if err := flags.Parse(commandArgs); err != nil {
			return err
		}
		result, err := store.Maintain(ctx, cfg, time.Now())
		if err != nil {
			return err
		}
		if *jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(result)
		}
		fmt.Printf("维护完成：流量汇总 %d，系统汇总 %d，清理分钟流量 %d，系统样本 %d，事件 %d，过期汇总 %d。\n",
			result.TrafficRollups, result.SystemRollups, result.TrafficDeleted,
			result.SystemDeleted, result.EventsDeleted, result.RollupsDeleted)
		return nil
	case "backup":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		directory := flags.String("dir", "/var/backups/vps-node-auditor/database", "private backup directory")
		if err := flags.Parse(commandArgs); err != nil {
			return err
		}
		name, err := uniqueBackupName(time.Now().UTC())
		if err != nil {
			return err
		}
		destination := filepath.Join(*directory, name)
		if err := store.Backup(ctx, destination); err != nil {
			return err
		}
		removed, err := pruneBackups(*directory, cfg.Retention.BackupCount)
		if err != nil {
			return err
		}
		fmt.Printf("一致性备份已生成：%s（清理旧备份 %d 份）\n", destination, removed)
		return nil
	case "integrity":
		if len(commandArgs) != 0 {
			return errors.New("integrity does not accept arguments")
		}
		if err := store.IntegrityCheck(ctx); err != nil {
			return err
		}
		fmt.Println("数据库完整性检查：正常")
		return nil
	case "observe":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		output := flags.String("output", "/var/lib/vps-node-auditor/acceptance.ndjson", "private NDJSON output")
		backupDirectory := flags.String("backup-dir", "/var/backups/vps-node-auditor/database", "backup directory")
		if err := flags.Parse(commandArgs); err != nil {
			return err
		}
		return writeObservation(ctx, store, cfg.Database.Path, *backupDirectory, *output)
	default:
		return fmt.Errorf("unknown command %q; use maintain, backup, integrity, or observe", command)
	}
}

func uniqueBackupName(now time.Time) (string, error) {
	suffix := make([]byte, 4)
	if _, err := cryptorand.Read(suffix); err != nil {
		return "", errors.New("generate unique backup name")
	}
	return fmt.Sprintf("audit-%s-%x.db", now.Format("20060102T150405Z"), suffix), nil
}

type observation struct {
	ObservedAt       time.Time `json:"observed_at"`
	CollectorState   string    `json:"collector_state"`
	MemoryCurrent    int64     `json:"memory_current_bytes"`
	CPUUsageNSec     int64     `json:"cpu_usage_nsec"`
	NRestarts        int64     `json:"n_restarts"`
	DatabaseBytes    int64     `json:"database_bytes"`
	WALBytes         int64     `json:"wal_bytes"`
	BackupCount      int       `json:"backup_count"`
	LatestBackup     string    `json:"latest_backup,omitempty"`
	RecentFailures   int64     `json:"recent_failures"`
	ConsecutiveFails int64     `json:"consecutive_failures"`
	SystemMissing    string    `json:"system_missing_sources,omitempty"`
}

func writeObservation(ctx context.Context, store *storage.Store, databasePath, backupDirectory, outputPath string) error {
	health, err := store.Health(ctx, time.Now())
	if err != nil {
		return err
	}
	item := observation{ObservedAt: time.Now().UTC(), RecentFailures: health.RecentFailures,
		ConsecutiveFails: health.ConsecutiveFailures, SystemMissing: health.SystemMissing}
	item.DatabaseBytes = fileSize(databasePath)
	item.WALBytes = fileSize(databasePath + "-wal")
	serviceOutput, err := exec.CommandContext(ctx, "systemctl", "show", "node-audit-collector",
		"--property", "ActiveState", "--property", "MemoryCurrent",
		"--property", "CPUUsageNSec", "--property", "NRestarts").Output()
	if err != nil {
		return errors.New("read collector resource state")
	}
	for _, line := range strings.Split(string(serviceOutput), "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		switch key {
		case "ActiveState":
			item.CollectorState = value
		case "MemoryCurrent":
			item.MemoryCurrent, _ = strconv.ParseInt(value, 10, 64)
		case "CPUUsageNSec":
			item.CPUUsageNSec, _ = strconv.ParseInt(value, 10, 64)
		case "NRestarts":
			item.NRestarts, _ = strconv.ParseInt(value, 10, 64)
		}
	}
	if entries, err := os.ReadDir(backupDirectory); err == nil {
		var names []string
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasPrefix(entry.Name(), "audit-") && strings.HasSuffix(entry.Name(), ".db") {
				names = append(names, entry.Name())
			}
		}
		sort.Strings(names)
		item.BackupCount = len(names)
		if len(names) > 0 {
			item.LatestBackup = names[len(names)-1]
		}
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return fmt.Errorf("create observation directory: %w", err)
	}
	file, err := os.OpenFile(outputPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open observation output: %w", err)
	}
	defer file.Close()
	if err := os.Chmod(outputPath, 0o600); err != nil {
		return fmt.Errorf("restrict observation output: %w", err)
	}
	encoder := json.NewEncoder(file)
	if err := encoder.Encode(item); err != nil {
		return fmt.Errorf("append observation: %w", err)
	}
	fmt.Println("7 天验收快照已追加")
	return nil
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func splitArgs(args []string) (string, string, []string, error) {
	configPath := "/etc/vps-node-auditor/config.json"
	for len(args) > 0 {
		if args[0] == "--config" {
			if len(args) < 2 {
				return "", "", nil, errors.New("--config requires a path")
			}
			configPath, args = args[1], args[2:]
			continue
		}
		if strings.HasPrefix(args[0], "--config=") {
			configPath, args = strings.TrimPrefix(args[0], "--config="), args[1:]
			continue
		}
		return configPath, args[0], args[1:], nil
	}
	return "", "", nil, errors.New("a command is required")
}

func pruneBackups(directory string, keep int) (int, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0, fmt.Errorf("read backup directory: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "audit-") && strings.HasSuffix(entry.Name(), ".db") {
			names = append(names, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	removed := 0
	if keep >= len(names) {
		return 0, nil
	}
	for _, name := range names[keep:] {
		if err := os.Remove(filepath.Join(directory, name)); err != nil {
			return removed, fmt.Errorf("remove expired backup: %w", err)
		}
		removed++
	}
	return removed, nil
}
