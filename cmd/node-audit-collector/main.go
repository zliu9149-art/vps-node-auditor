// Command node-audit-collector continuously converts cumulative proxy
// statistics into auditable local deltas stored in SQLite.
// 中文：node-audit-collector 持续把代理核心的累计计数器转换为可审计增量，
// 并写入本地 SQLite 数据库。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"vps-node-auditor/internal/collector"
	"vps-node-auditor/internal/config"
	"vps-node-auditor/internal/host"
	"vps-node-auditor/internal/source"
	"vps-node-auditor/internal/storage"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Printf("node-audit-collector: %v", err)
		os.Exit(1)
	}
}

// run initializes the configured source and store, performs an immediate
// collection, and then waits for either the next interval or process shutdown.
// 中文：run 初始化数据源与存储，立即采集一次，随后等待下一个周期或进程退出信号。
func run(args []string) error {
	flags := flag.NewFlagSet("node-audit-collector", flag.ContinueOnError)
	configPath := flags.String("config", "config.json", "path to the local JSON configuration")
	once := flags.Bool("once", false, "collect exactly one snapshot and exit")
	if err := flags.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	store, err := storage.Open(cfg.Database.Path, false)
	if err != nil {
		return err
	}
	defer store.Close()

	// systemd sends SIGTERM during an orderly stop. Carrying the signal through
	// the context lets an in-flight gRPC request finish or cancel cleanly.
	// 中文：systemd 正常停止服务时发送 SIGTERM；通过 context 传递信号，
	// 可让正在进行的 gRPC 请求正常完成或取消。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := store.Migrate(ctx); err != nil {
		return err
	}
	if err := store.SyncCredentials(ctx, cfg.Credentials); err != nil {
		return err
	}

	statsSource, err := buildSource(cfg, store)
	if err != nil {
		return err
	}
	logger := log.New(os.Stderr, "node-audit-collector: ", log.LstdFlags|log.LUTC)
	worker := collector.New(statsSource, store).WithCycleLock(cfg.Collector.ConfigLockPath)
	if !cfg.Host.Disabled {
		worker.WithHostSource(host.NewLinuxSource(
			cfg.Host.Interface,
			cfg.Collector.V2RayAPI.ProcessName,
			cfg.HostCommandTimeout(),
		))
	}
	worker.WithAfterCollect(func(ctx context.Context, now time.Time) error {
		if err := store.EvaluateAlerts(ctx, cfg, now); err != nil {
			return err
		}
		events, err := store.Events(ctx, now.Add(-time.Second), now.Add(time.Second))
		if err != nil {
			return err
		}
		for _, event := range events {
			if event.Category == "alert" {
				logger.Printf("告警：%s", event.Message)
			}
		}
		return nil
	})
	if *once {
		return worker.CollectOnce(ctx)
	}

	collect := func() {
		if err := worker.CollectOnce(ctx); err != nil {
			if errors.Is(err, storage.ErrStaleSnapshot) {
				logger.Printf("已跳过过期快照")
				return
			}
			// Errors are safe to journal because source and storage layers never
			// include UUID values in returned messages.
			// 中文：数据源和存储层返回的错误不包含 UUID，因此可以安全写入 journal。
			logger.Printf("采集失败：%v", err)
		}
	}
	// Collect immediately so service health does not remain unknown for a full
	// interval after startup.
	// 中文：服务启动后立即采集，避免健康状态在整个首个周期内保持未知。
	collect()

	ticker := time.NewTicker(cfg.Interval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			collect()
		}
	}
}

// buildSource constructs the configured statistics adapter without exposing
// transport or credential details to the scheduling loop.
// 中文：buildSource 根据配置构造统计适配器，不向调度循环暴露传输层或凭据细节。
func buildSource(cfg config.Config, store *storage.Store) (source.StatsSource, error) {
	switch cfg.Collector.Source {
	case "mock":
		return source.NewMockFileSource(cfg.Collector.MockFile), nil
	case "sing-box-v2ray":
		// The source needs UUIDs only as opaque database keys. Returned errors
		// deliberately identify array positions rather than map keys or values.
		// 中文：数据源只把 UUID 当作不透明数据库键；错误仅报告数组位置，
		// 不返回映射键或具体值。
		return source.NewV2RaySource(
			cfg.Collector.V2RayAPI.Address,
			cfg.CollectorTimeout(),
			store.StatsMappings,
		), nil
	default:
		return nil, fmt.Errorf("unsupported collector source %q", cfg.Collector.Source)
	}
}
