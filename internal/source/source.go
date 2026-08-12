// Package source defines statistics inputs used by the collector.
// 中文：source 包定义采集器可使用的统计数据输入接口。
package source

import (
	"context"

	"vps-node-auditor/internal/domain"
)

// StatsSource returns one cumulative proxy and host observation. Implementations
// must not persist or log credential UUID values.
// 中文：StatsSource 返回一次代理和主机累计观测；实现不得持久化或记录凭据 UUID。
type StatsSource interface {
	// Name returns a stable namespace for persisted collector state.
	// 中文：Name 返回用于持久化采集状态的稳定命名空间。
	Name() string
	// Collect observes cumulative statistics without resetting source counters.
	// 中文：Collect 读取累计统计，但不得重置数据源计数器。
	Collect(context.Context) (domain.StatsSnapshot, error)
}
