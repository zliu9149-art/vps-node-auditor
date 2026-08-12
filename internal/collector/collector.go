// Package collector coordinates one statistics source with persistent audit
// storage. It contains no networking or scheduling policy.
// 中文：collector 包协调统计数据源与持久化审计存储，本身不包含网络和调度策略。
package collector

import (
	"context"
	"errors"
	"fmt"
	"time"

	"vps-node-auditor/internal/domain"
	"vps-node-auditor/internal/lockfile"
	"vps-node-auditor/internal/source"
	"vps-node-auditor/internal/storage"
)

// Repository describes the storage operations required by the collector.
// 中文：Repository 定义采集器所需的最小存储操作集合。
type Repository interface {
	// RecordSnapshot persists one successful source observation atomically.
	// 中文：RecordSnapshot 以原子事务保存一次成功观测。
	RecordSnapshot(context.Context, string, domain.StatsSnapshot) error
	// RecordFailure records an evidence gap without advancing counter state.
	// 中文：RecordFailure 记录证据缺口，但不推进累计计数器状态。
	RecordFailure(context.Context, string, time.Time) error
}

// HostRepository is implemented by stores that persist host-wide evidence.
// 中文：HostRepository 由能够保存主机级证据的存储实现。
type HostRepository interface {
	RecordHostSnapshot(context.Context, string, domain.HostSnapshot) error
}

// HostSource captures partial host evidence without failing proxy collection.
// 中文：HostSource 采集部分主机证据，但不会导致代理采集失败。
type HostSource interface {
	Collect(context.Context) domain.HostSnapshot
}

// Collector records observations from one named statistics source.
// 中文：Collector 记录来自一个具名统计数据源的观测结果。
type Collector struct {
	source   source.StatsSource
	store    Repository
	host     HostSource
	after    func(context.Context, time.Time) error
	lockPath string
	now      func() time.Time
}

// WithCycleLock makes each collection cycle mutually exclusive with a
// owner write or maintenance operation. An occupied lock skips the cycle without creating
// a false evidence gap.
// 中文：WithCycleLock 使每次采集周期与 provision 应用互斥；锁被占用时跳过该周期，
// 且不会制造虚假的证据缺口。
func (c *Collector) WithCycleLock(path string) *Collector {
	c.lockPath = path
	return c
}

// WithAfterCollect installs a local post-cycle evaluator such as alert state
// handling. It runs after both independent evidence paths have been attempted.
// 中文：WithAfterCollect 安装本地周期后评估器（例如告警状态处理）；它会在两个
// 独立证据路径都尝试完成后运行。
func (c *Collector) WithAfterCollect(after func(context.Context, time.Time) error) *Collector {
	c.after = after
	return c
}

// WithHostSource enables independent Linux host evidence collection.
// 中文：WithHostSource 启用独立的 Linux 主机证据采集。
func (c *Collector) WithHostSource(hostSource HostSource) *Collector {
	c.host = hostSource
	return c
}

// New creates a collector with a real UTC clock.
// 中文：New 使用真实 UTC 时钟创建采集器。
func New(statsSource source.StatsSource, store Repository) *Collector {
	return &Collector{source: statsSource, store: store, now: time.Now}
}

// CollectOnce records one observation. Source errors are converted into an
// explicit database gap before they are returned to the service journal.
// 中文：CollectOnce 记录一次观测；数据源错误会先转化为数据库中的明确缺口，
// 然后再返回给服务日志。
func (c *Collector) CollectOnce(ctx context.Context) error {
	if c.lockPath != "" {
		cycleLock, busy, err := lockfile.TryAcquire(c.lockPath)
		if busy {
			return nil
		}
		if err != nil {
			return fmt.Errorf("acquire collection cycle lock: %w", err)
		}
		defer cycleLock.Close()
	}
	var result error
	snapshot, trafficErr := c.source.Collect(ctx)
	if trafficErr != nil {
		// Preserve both failures when collection and gap recording fail. Losing
		// either error would hide whether the audit timeline is complete.
		// 中文：采集和缺口记录同时失败时保留两个错误，避免掩盖审计时间线是否完整。
		recordErr := c.store.RecordFailure(ctx, c.source.Name(), c.now().UTC())
		if recordErr != nil {
			result = errors.Join(fmt.Errorf("collect %s statistics: %w", c.source.Name(), trafficErr), recordErr)
		} else {
			result = fmt.Errorf("collect %s statistics: %w", c.source.Name(), trafficErr)
		}
	} else if err := c.store.RecordSnapshot(ctx, c.source.Name(), snapshot); err != nil {
		if errors.Is(err, storage.ErrStaleSnapshot) {
			result = err
		} else {
			result = fmt.Errorf("record %s snapshot: %w", c.source.Name(), err)
		}
	}
	if c.host != nil {
		hostStore, ok := c.store.(HostRepository)
		if !ok {
			result = errors.Join(result, errors.New("store does not support host snapshots"))
		} else if err := hostStore.RecordHostSnapshot(ctx, "linux-host", c.host.Collect(ctx)); err != nil {
			result = errors.Join(result, fmt.Errorf("record host snapshot: %w", err))
		}
	}
	if c.after != nil {
		if err := c.after(ctx, c.now().UTC()); err != nil {
			result = errors.Join(result, fmt.Errorf("evaluate post-collection state: %w", err))
		}
	}
	return result
}
