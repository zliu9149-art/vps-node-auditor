package source

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"vps-node-auditor/internal/domain"
)

const queryStatsMethod = "/v2ray.core.app.stats.command.StatsService/QueryStats"

// V2RaySource reads cumulative per-user counters from sing-box's loopback
// V2Ray Stats API. It never resets counters or logs statistical user names.
// 中文：V2RaySource 从 sing-box 回环 V2Ray Stats API 读取用户累计计数器，
// 绝不重置计数器，也不记录统计用户名。
type V2RaySource struct {
	address  string
	timeout  time.Duration
	mappings func(context.Context) (map[string]string, error)
	procRoot string
	mainPID  func(context.Context) (string, error)
	now      func() time.Time
}

// NewV2RaySource creates a loopback gRPC statistics source. mappings is called
// for every observation so credential changes take effect without restarting
// the collector.
// 中文：NewV2RaySource 创建回环 gRPC 数据源；每次观测都会调用 mappings，
// 因而凭据变更无需重启采集器即可生效。
func NewV2RaySource(address string, timeout time.Duration, mappings func(context.Context) (map[string]string, error)) *V2RaySource {
	return &V2RaySource{
		address:  address,
		timeout:  timeout,
		mappings: mappings,
		procRoot: "/proc",
		mainPID: func(ctx context.Context) (string, error) {
			output, err := exec.CommandContext(ctx, "systemctl", "show", "sing-box.service",
				"--property=MainPID", "--value").Output()
			return strings.TrimSpace(string(output)), err
		},
		now: time.Now,
	}
}

// Name returns the stable collector-state namespace for sing-box counters.
// 中文：Name 返回 sing-box 计数器使用的稳定采集状态命名空间。
func (s *V2RaySource) Name() string {
	return "sing-box-v2ray"
}

// Collect queries cumulative counters without resetting them. A process
// identity derived from procfs lets storage distinguish a verified core restart
// from an unexplained counter regression.
// 中文：Collect 查询累计计数器且不重置；从 procfs 推导的进程身份可帮助存储层
// 区分已确认核心重启和无法解释的计数器回退。
func (s *V2RaySource) Collect(ctx context.Context) (domain.StatsSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return domain.StatsSnapshot{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	statsUserUUID, err := s.mappings(callCtx)
	if err != nil {
		return domain.StatsSnapshot{}, errors.New("load enabled statistics mappings")
	}

	// reset=false is a core accounting invariant: the auditor observes the
	// proxy's cumulative counters but never mutates them.
	// 中文：reset=false 是核心计费不变量，审计器只观察累计计数器，绝不修改它们。
	request := &queryStatsRequest{Patterns: []string{"user>>>"}, Reset: false}
	response, err := queryV2RayStats(callCtx, s.address, request)
	if err != nil {
		return domain.StatsSnapshot{}, err
	}
	users, err := mapV2RayCounters(response.Stats, statsUserUUID)
	if err != nil {
		return domain.StatsSnapshot{}, err
	}
	// Counter collection remains useful when procfs is temporarily unavailable.
	// Storage then treats any regression as an unverified gap instead of a reset.
	// 中文：procfs 暂时不可用时仍保留计数器观测；存储层会把回退视为未确认缺口，
	// 而不是已确认重置。
	coreBootID := ""
	if pid, err := s.mainPID(callCtx); err == nil {
		coreBootID, _ = readProcessIdentity(s.procRoot, pid)
	}
	return domain.StatsSnapshot{
		ObservedAt: s.now().UTC(),
		CoreBootID: coreBootID,
		Users:      users,
	}, nil
}

// ProbeV2RayAPI performs a non-resetting gRPC query to verify that the Stats
// service, not merely its TCP listener, is available.
// 中文：ProbeV2RayAPI 通过不重置计数器的 gRPC 查询确认 Stats 服务本身可用，
// 而不只是 TCP 端口处于监听状态。
func ProbeV2RayAPI(ctx context.Context, address string, timeout time.Duration) error {
	_, err := ListV2RayUsers(ctx, address, timeout)
	return err
}

// ListV2RayUsers returns only opaque Stats user names from a non-resetting
// loopback query. Callers must compare them without printing the names.
func ListV2RayUsers(ctx context.Context, address string, timeout time.Duration) ([]string, error) {
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request := &queryStatsRequest{Patterns: []string{"user>>>"}, Reset: false}
	response, err := queryV2RayStats(callCtx, address, request)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	for _, stat := range response.Stats {
		parts := strings.Split(stat.Name, ">>>")
		if len(parts) == 4 && parts[0] == "user" && parts[1] != "" && parts[2] == "traffic" {
			seen[parts[1]] = struct{}{}
		}
	}
	users := make([]string, 0, len(seen))
	for name := range seen {
		users = append(users, name)
	}
	sort.Strings(users)
	return users, nil
}

const maxGRPCResponseBytes = 16 << 20

func queryV2RayStats(ctx context.Context, address string, request *queryStatsRequest) (*queryStatsResponse, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return nil, errors.New("V2Ray Stats address must use a loopback IP")
	}
	payload, err := (v2RayCodec{}).Marshal(request)
	if err != nil {
		return nil, errors.New("encode V2Ray Stats request")
	}
	frame := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	transport := &http.Transport{
		Protocols: protocols,
		DialContext: func(ctx context.Context, network, target string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, target)
		},
	}
	defer transport.CloseIdleConnections()
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://"+address+queryStatsMethod, bytes.NewReader(frame))
	if err != nil {
		return nil, errors.New("create loopback V2Ray Stats request")
	}
	httpRequest.Header.Set("Content-Type", "application/grpc")
	httpRequest.Header.Set("TE", "trailers")
	response, err := transport.RoundTrip(httpRequest)
	if err != nil {
		return nil, errors.New("query loopback V2Ray Stats API")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("V2Ray Stats API returned a non-success HTTP status")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxGRPCResponseBytes+6))
	if err != nil {
		return nil, errors.New("read V2Ray Stats response")
	}
	if len(body) > maxGRPCResponseBytes+5 {
		return nil, errors.New("V2Ray Stats response exceeds the size limit")
	}
	grpcStatus := response.Trailer.Get("Grpc-Status")
	if grpcStatus == "" {
		grpcStatus = response.Header.Get("Grpc-Status")
	}
	if grpcStatus != "0" {
		return nil, errors.New("V2Ray Stats API returned a non-success gRPC status")
	}
	message, err := decodeUnaryGRPCFrame(body)
	if err != nil {
		return nil, err
	}
	result := &queryStatsResponse{}
	if err := (v2RayCodec{}).Unmarshal(message, result); err != nil {
		return nil, errors.New("decode V2Ray Stats response")
	}
	return result, nil
}

func decodeUnaryGRPCFrame(data []byte) ([]byte, error) {
	if len(data) < 5 {
		return nil, errors.New("V2Ray Stats response has an incomplete gRPC frame")
	}
	if data[0] != 0 {
		return nil, errors.New("compressed V2Ray Stats responses are unsupported")
	}
	length := int(binary.BigEndian.Uint32(data[1:5]))
	if length > maxGRPCResponseBytes || length != len(data)-5 {
		return nil, errors.New("V2Ray Stats response has an invalid gRPC frame length")
	}
	return data[5:], nil
}

// mapV2RayCounters accepts only configured user traffic counters, rejects
// ambiguous duplicate directions, and returns UUID-keyed cumulative values.
// 中文：mapV2RayCounters 只接收已配置用户的流量计数器，拒绝重复方向，
// 并返回以 UUID 为内部键的累计值。
func mapV2RayCounters(stats []v2RayStat, statsUserUUID map[string]string) (map[string]domain.Counters, error) {
	users := make(map[string]domain.Counters, len(statsUserUUID))
	for _, uuid := range statsUserUUID {
		users[uuid] = domain.Counters{}
	}
	seen := make(map[string]map[string]bool, len(statsUserUUID))
	for index, stat := range stats {
		parts := strings.Split(stat.Name, ">>>")
		if len(parts) != 4 || parts[0] != "user" || parts[2] != "traffic" {
			continue
		}
		uuid, configured := statsUserUUID[parts[1]]
		if !configured {
			continue
		}
		if stat.Value < 0 {
			return nil, fmt.Errorf("V2Ray stat[%d] contains a negative counter", index)
		}
		direction := parts[3]
		if direction != "uplink" && direction != "downlink" {
			continue
		}
		if seen[uuid] == nil {
			seen[uuid] = make(map[string]bool, 2)
		}
		if seen[uuid][direction] {
			return nil, fmt.Errorf("V2Ray stat[%d] duplicates a configured counter direction", index)
		}
		seen[uuid][direction] = true
		counter := users[uuid]
		if direction == "uplink" {
			counter.Upload = stat.Value
		} else {
			counter.Download = stat.Value
		}
		users[uuid] = counter
	}
	return users, nil
}

// readProcessIdentity combines the kernel boot ID, systemd MainPID, and Linux
// process start ticks so PID reuse cannot be mistaken for the same core epoch.
// 中文：readProcessIdentity 组合内核启动 ID、匹配 PID 和进程启动时钟，
// 防止 PID 重用被误判为同一核心运行周期。
func readProcessIdentity(procRoot, pid string) (string, error) {
	if value, err := strconv.Atoi(pid); err != nil || value <= 0 {
		return "", errors.New("sing-box MainPID is unavailable")
	}
	bootID, err := os.ReadFile(filepath.Join(procRoot, "sys", "kernel", "random", "boot_id"))
	if err != nil {
		return "", errors.New("read kernel boot identity")
	}
	stat, err := os.ReadFile(filepath.Join(procRoot, pid, "stat"))
	if err != nil {
		return "", errors.New("read sing-box MainPID stat")
	}
	closingParen := strings.LastIndexByte(string(stat), ')')
	if closingParen < 0 {
		return "", errors.New("parse sing-box MainPID stat")
	}
	// Removing pid and comm leaves Linux proc stat field 3 at index 0;
	// index 19 is therefore field 22, the process start time in clock ticks.
	// 中文：去掉 pid 和 comm 后，Linux proc stat 的第 3 字段对应索引 0，
	// 因此索引 19 即第 22 字段，也就是以时钟滴答表示的进程启动时间。
	fields := strings.Fields(string(stat)[closingParen+1:])
	if len(fields) <= 19 {
		return "", errors.New("parse sing-box MainPID start time")
	}
	return strings.TrimSpace(string(bootID)) + ":" + pid + ":" + fields[19], nil
}
