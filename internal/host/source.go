// Package host collects bounded, read-only Linux host evidence.
// 中文：host 包采集范围受控、只读的 Linux 主机证据。
package host

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"vps-node-auditor/internal/domain"
)

// CommandRunner executes only arguments selected by LinuxSource.
// 中文：CommandRunner 只执行 LinuxSource 内部选定的固定参数。
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// LinuxSource reads procfs and invokes vnStat/iproute2 with fixed arguments.
// Individual evidence failures are returned as MissingSources, not as a fatal
// error, so host telemetry can never block proxy traffic accounting.
// 中文：LinuxSource 读取 procfs，并以固定参数调用 vnStat/iproute2。单项证据失败
// 会写入 MissingSources，而不是作为致命错误返回，因此不会阻断代理流量记账。
type LinuxSource struct {
	InterfaceName  string
	ProxyProcess   string
	ProcRoot       string
	SysRoot        string
	Runner         CommandRunner
	CommandTimeout time.Duration
	Now            func() time.Time
}

// NewLinuxSource creates a production Linux host source.
// 中文：NewLinuxSource 创建生产用 Linux 主机数据源。
func NewLinuxSource(interfaceName, proxyProcess string, commandTimeout time.Duration) *LinuxSource {
	return &LinuxSource{
		InterfaceName:  interfaceName,
		ProxyProcess:   proxyProcess,
		ProcRoot:       "/proc",
		SysRoot:        "/sys",
		Runner:         execRunner{},
		CommandTimeout: commandTimeout,
		Now:            time.Now,
	}
}

// Collect captures every available evidence class and reports missing classes
// in a stable, comma-sortable list.
// 中文：Collect 采集所有可用证据，并以稳定、可排序的列表报告缺失类别。
func (s *LinuxSource) Collect(ctx context.Context) domain.HostSnapshot {
	result := domain.HostSnapshot{
		ObservedAt:    s.Now().UTC(),
		InterfaceName: s.InterfaceName,
	}
	missing := make(map[string]struct{})
	markMissing := func(name string) { missing[name] = struct{}{} }

	if data, err := os.ReadFile(filepath.Join(s.ProcRoot, "sys", "kernel", "random", "boot_id")); err == nil {
		result.HostBootID = strings.TrimSpace(string(data))
	} else {
		markMissing("boot_id")
	}
	if data, err := os.ReadFile(filepath.Join(s.ProcRoot, "stat")); err == nil {
		result.CPUTotal, result.CPUIdle, result.CPUValid = parseCPUStat(data)
		if !result.CPUValid {
			markMissing("proc_stat")
		}
	} else {
		markMissing("proc_stat")
	}
	if data, err := os.ReadFile(filepath.Join(s.ProcRoot, "loadavg")); err == nil {
		if load, ok := parseLoad(data); ok {
			result.Load1 = load
		} else {
			markMissing("loadavg")
		}
	} else {
		markMissing("loadavg")
	}
	if data, err := os.ReadFile(filepath.Join(s.ProcRoot, "meminfo")); err == nil {
		result.MemoryUsedBytes, result.SwapUsedBytes, result.MemoryValid = parseMemInfo(data)
		if !result.MemoryValid {
			markMissing("meminfo")
		}
	} else {
		markMissing("meminfo")
	}
	if data, err := os.ReadFile(filepath.Join(s.ProcRoot, "net", "dev")); err == nil {
		result.NetworkRXTotal, result.NetworkTXTotal, result.InterfaceDrops, result.NetworkValid = parseNetDev(data, s.InterfaceName)
	}
	if !result.NetworkValid {
		result.NetworkRXTotal, result.NetworkTXTotal, result.InterfaceDrops, result.NetworkValid = readSysNet(s.SysRoot, s.InterfaceName)
	}
	if !result.NetworkValid {
		markMissing("netdev")
	}

	if data, err := s.run(ctx, "nstat", "-az"); err == nil {
		result.TCPRetransTotal, result.TCPResetsTotal, result.TCPValid = parseNStat(data)
		if !result.TCPValid {
			markMissing("nstat")
		}
	} else {
		markMissing("nstat")
	}
	if data, err := s.run(ctx, "ss", "-Htan", "state", "established"); err == nil {
		result.ActiveConnection = countNonEmptyLines(data)
		result.ConnectionsValid = true
	} else {
		markMissing("ss")
	}
	result.ProxyServiceOK, result.ProxyValid = processRunning(s.ProcRoot, s.ProxyProcess)
	if !result.ProxyValid {
		markMissing("sing_box_process")
	}
	if data, err := s.run(ctx, "vnstat", "--json", "f", "1", "--iface", s.InterfaceName); err == nil {
		if snapshot, err := parseVnStat(data, s.InterfaceName); err == nil {
			result.VnStat = &snapshot
		} else {
			markMissing("vnstat")
		}
	} else {
		markMissing("vnstat")
	}

	for name := range missing {
		result.MissingSources = append(result.MissingSources, name)
	}
	sort.Strings(result.MissingSources)
	return result
}

func (s *LinuxSource) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if s.CommandTimeout <= 0 {
		return s.Runner.Run(ctx, name, args...)
	}
	commandCtx, cancel := context.WithTimeout(ctx, s.CommandTimeout)
	defer cancel()
	return s.Runner.Run(commandCtx, name, args...)
}

func parseCPUStat(data []byte) (total, idle int64, ok bool) {
	line, _, _ := bytes.Cut(data, []byte("\n"))
	fields := strings.Fields(string(line))
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	values := make([]int64, 0, len(fields)-1)
	for _, field := range fields[1:] {
		value, err := strconv.ParseInt(field, 10, 64)
		if err != nil || value < 0 {
			return 0, 0, false
		}
		total += value
		values = append(values, value)
	}
	idle = values[3]
	if len(values) > 4 {
		idle += values[4]
	}
	return total, idle, total > 0
}

func parseLoad(data []byte) (float64, bool) {
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, false
	}
	value, err := strconv.ParseFloat(fields[0], 64)
	return value, err == nil && value >= 0
}

func parseMemInfo(data []byte) (memoryUsed, swapUsed int64, ok bool) {
	values := make(map[string]int64)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err == nil && value >= 0 {
			values[strings.TrimSuffix(fields[0], ":")] = value * 1024
		}
	}
	total, totalOK := values["MemTotal"]
	free, freeOK := values["MemFree"]
	if !totalOK || !freeOK || total < free {
		return 0, 0, false
	}
	memoryUsed = total - free - values["Buffers"] - values["Cached"] - values["SReclaimable"] + values["Shmem"]
	if memoryUsed < 0 {
		memoryUsed = 0
	}
	swapUsed = values["SwapTotal"] - values["SwapFree"]
	if swapUsed < 0 {
		swapUsed = 0
	}
	return memoryUsed, swapUsed, true
}

func parseNetDev(data []byte, interfaceName string) (rx, tx, drops int64, ok bool) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		left, right, found := strings.Cut(scanner.Text(), ":")
		if !found || strings.TrimSpace(left) != interfaceName {
			continue
		}
		fields := strings.Fields(right)
		if len(fields) < 16 {
			return 0, 0, 0, false
		}
		values := make([]int64, len(fields))
		for i, field := range fields {
			value, err := strconv.ParseInt(field, 10, 64)
			if err != nil || value < 0 {
				return 0, 0, 0, false
			}
			values[i] = value
		}
		return values[0], values[8], values[3] + values[11], true
	}
	return 0, 0, 0, false
}

func readSysNet(sysRoot, interfaceName string) (rx, tx, drops int64, ok bool) {
	statistics := filepath.Join(sysRoot, "class", "net", interfaceName, "statistics")
	read := func(name string) (int64, bool) {
		data, err := os.ReadFile(filepath.Join(statistics, name))
		if err != nil {
			return 0, false
		}
		value, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
		return value, err == nil && value >= 0
	}
	rx, rxOK := read("rx_bytes")
	tx, txOK := read("tx_bytes")
	rxDrops, rxDropsOK := read("rx_dropped")
	txDrops, txDropsOK := read("tx_dropped")
	return rx, tx, rxDrops + txDrops, rxOK && txOK && rxDropsOK && txDropsOK
}

func parseNStat(data []byte) (retransmits, resets int64, ok bool) {
	values := make(map[string]int64)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err == nil && value >= 0 {
			values[fields[0]] = value
		}
	}
	retransmits, retransOK := values["TcpRetransSegs"]
	establishedResets, resetOK := values["TcpEstabResets"]
	outResets := values["TcpOutRsts"]
	return retransmits, establishedResets + outResets, retransOK && resetOK
}

func countNonEmptyLines(data []byte) int64 {
	var count int64
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "" {
			count++
		}
	}
	return count
}

func processRunning(procRoot, processName string) (running, valid bool) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return false, false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(procRoot, entry.Name(), "comm"))
		if err == nil && strings.TrimSpace(string(data)) == processName {
			return true, true
		}
	}
	return false, true
}

type vnStatJSON struct {
	Interfaces []struct {
		Name    string `json:"name"`
		Created struct {
			Date struct {
				Year  int `json:"year"`
				Month int `json:"month"`
				Day   int `json:"day"`
			} `json:"date"`
			Time struct {
				Hour   int `json:"hour"`
				Minute int `json:"minute"`
			} `json:"time"`
		} `json:"created"`
		Traffic struct {
			Total struct {
				RX int64 `json:"rx"`
				TX int64 `json:"tx"`
			} `json:"total"`
		} `json:"traffic"`
	} `json:"interfaces"`
}

func parseVnStat(data []byte, interfaceName string) (domain.VnStatSnapshot, error) {
	var payload vnStatJSON
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&payload); err != nil {
		return domain.VnStatSnapshot{}, errors.New("decode vnStat JSON")
	}
	for _, item := range payload.Interfaces {
		if item.Name != interfaceName {
			continue
		}
		if item.Traffic.Total.RX < 0 || item.Traffic.Total.TX < 0 {
			return domain.VnStatSnapshot{}, errors.New("vnStat totals are negative")
		}
		created := item.Created
		coverage := time.Time{}
		if created.Date.Year > 0 && created.Date.Month >= 1 && created.Date.Month <= 12 && created.Date.Day > 0 {
			coverage = time.Date(created.Date.Year, time.Month(created.Date.Month), created.Date.Day,
				created.Time.Hour, created.Time.Minute, 0, 0, time.Local).UTC()
		}
		return domain.VnStatSnapshot{
			InterfaceName: interfaceName,
			RXTotal:       item.Traffic.Total.RX,
			TXTotal:       item.Traffic.Total.TX,
			CoverageStart: coverage,
			CollectionOK:  true,
		}, nil
	}
	return domain.VnStatSnapshot{}, fmt.Errorf("vnStat interface is unavailable")
}
