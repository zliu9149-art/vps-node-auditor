package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"vps-node-auditor/internal/domain"
)

// MockFileSource reads cumulative counters from a local JSON file. It keeps
// reset, gap, and query regression tests independent of a live VPS.
// 中文：MockFileSource 从本地 JSON 读取累计计数器，使重置、缺口和查询回归测试
// 不依赖真实 VPS。
type MockFileSource struct {
	path string
	now  func() time.Time
}

// NewMockFileSource creates a deterministic local statistics source.
// 中文：NewMockFileSource 创建结果可重复的本地统计数据源。
func NewMockFileSource(path string) *MockFileSource {
	return &MockFileSource{path: path, now: time.Now}
}

// Name returns the stable collector-state namespace for mock observations.
// 中文：Name 返回模拟观测使用的稳定采集状态命名空间。
func (s *MockFileSource) Name() string {
	return "mock"
}

// Collect decodes one snapshot. An omitted observed_at uses the current UTC
// time, which makes the same fixture useful for a continuously running demo.
// 中文：Collect 解码一个快照；缺少 observed_at 时使用当前 UTC，便于固定夹具
// 同时用于连续运行演示。
func (s *MockFileSource) Collect(ctx context.Context) (domain.StatsSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return domain.StatsSnapshot{}, err
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return domain.StatsSnapshot{}, fmt.Errorf("read mock stats: %w", err)
	}
	var input mockSnapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return domain.StatsSnapshot{}, fmt.Errorf("decode mock stats: %w", err)
	}
	if len(input.Users) == 0 {
		return domain.StatsSnapshot{}, errors.New("mock stats must include at least one user")
	}

	observedAt := s.now().UTC()
	if input.ObservedAt != "" {
		observedAt, err = time.Parse(time.RFC3339, input.ObservedAt)
		if err != nil {
			return domain.StatsSnapshot{}, fmt.Errorf("parse mock observed_at: %w", err)
		}
		observedAt = observedAt.UTC()
	}
	users := make(map[string]domain.Counters, len(input.Users))
	for i, user := range input.Users {
		if user.UUID == "" {
			return domain.StatsSnapshot{}, fmt.Errorf("mock users[%d].uuid is required", i)
		}
		if user.Upload < 0 || user.Download < 0 {
			return domain.StatsSnapshot{}, fmt.Errorf("mock users[%d] counters must not be negative", i)
		}
		if _, exists := users[user.UUID]; exists {
			return domain.StatsSnapshot{}, fmt.Errorf("mock users[%d] duplicates a UUID", i)
		}
		users[user.UUID] = domain.Counters{Upload: user.Upload, Download: user.Download}
	}
	return domain.StatsSnapshot{
		ObservedAt: observedAt,
		CoreBootID: input.CoreBootID,
		Users:      users,
		System:     input.System,
	}, nil
}

type mockSnapshot struct {
	ObservedAt string                 `json:"observed_at,omitempty"`
	CoreBootID string                 `json:"core_boot_id"`
	Users      []mockUser             `json:"users"`
	System     *domain.SystemSnapshot `json:"system,omitempty"`
}

// mockUser mirrors only the cumulative fields required by the production
// StatsSource contract; fixture validation never includes UUID values in errors.
// 中文：mockUser 只保留生产 StatsSource 契约需要的累计字段；夹具校验错误不包含 UUID。
type mockUser struct {
	UUID     string `json:"uuid"`
	Upload   int64  `json:"upload_total"`
	Download int64  `json:"download_total"`
}
