// Package provision implements owner-only credential changes.
// 中文：provision 包实现仅供所有者使用的凭据变更。
package provision

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Config contains VPS-only public connection metadata and local production
// paths. Despite the name "public key", the complete file remains private
// operational configuration and must be mode 0600.
// 中文：Config 包含仅存在于 VPS 的连接元数据和生产路径；即使 Reality 字段名为
// “公钥”，完整文件仍属于私密运维配置，权限必须为 0600。
type Config struct {
	CoreConfigPath   string `json:"core_config_path"`
	CoreBinaryPath   string `json:"core_binary_path"`
	CoreUnitPath     string `json:"core_unit_path"`
	ServiceName      string `json:"service_name"`
	InboundTag       string `json:"inbound_tag"`
	OutputDirectory  string `json:"output_directory"`
	BackupDirectory  string `json:"backup_directory"`
	LockPath         string `json:"lock_path"`
	Server           string `json:"server"`
	Port             int    `json:"port"`
	RealityPublicKey string `json:"reality_public_key"`
	SNI              string `json:"sni"`
	ShortID          string `json:"short_id"`
	Flow             string `json:"flow"`
	MixedPort        int    `json:"mixed_port"`
	StatsAddress     string `json:"stats_address"`
}

// LoadConfig reads, defaults, validates, and permission-checks provision JSON.
// Error messages never echo field values.
// 中文：LoadConfig 读取、补默认值、校验并检查 provision JSON 权限；错误信息
// 绝不回显字段值。
func LoadConfig(path string) (Config, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Config{}, fmt.Errorf("inspect provision config: %w", err)
	}
	if runtime.GOOS != "windows" && permissionsBroaderThan(info.Mode().Perm(), 0o600) {
		return Config{}, errors.New("provision config permissions must be 0600 or stricter")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read provision config: %w", err)
	}
	var cfg Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode provision config: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("decode provision config: trailing JSON values are not allowed")
	}
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return Config{}, fmt.Errorf("resolve provision config directory: %w", err)
	}
	resolve := func(value string) string {
		if value == "" || filepath.IsAbs(value) {
			return filepath.Clean(value)
		}
		return filepath.Join(base, value)
	}
	cfg.CoreConfigPath = resolve(cfg.CoreConfigPath)
	cfg.CoreBinaryPath = resolve(cfg.CoreBinaryPath)
	cfg.CoreUnitPath = resolve(cfg.CoreUnitPath)
	cfg.OutputDirectory = resolve(cfg.OutputDirectory)
	cfg.BackupDirectory = resolve(cfg.BackupDirectory)
	cfg.LockPath = resolve(cfg.LockPath)
	if cfg.ServiceName == "" {
		cfg.ServiceName = "sing-box"
	}
	if cfg.InboundTag == "" {
		cfg.InboundTag = "proxy-in"
	}
	if cfg.Port == 0 {
		cfg.Port = 443
	}
	if cfg.Flow == "" {
		cfg.Flow = "xtls-rprx-vision"
	}
	if cfg.MixedPort == 0 {
		cfg.MixedPort = 7890
	}
	if cfg.StatsAddress == "" {
		cfg.StatsAddress = "127.0.0.1:10085"
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func permissionsBroaderThan(actual, maximum os.FileMode) bool {
	return actual&^maximum != 0
}

// Validate rejects unsafe paths and unsupported automatic service behavior.
// 中文：Validate 拒绝不安全路径和未支持的自动服务行为。
func (c Config) Validate() error {
	if c.CoreConfigPath == "" || c.CoreBinaryPath == "" || c.CoreUnitPath == "" ||
		c.OutputDirectory == "" || c.BackupDirectory == "" || c.LockPath == "" {
		return errors.New("all provision paths are required")
	}
	if c.ServiceName != "sing-box" {
		return errors.New("service_name must be sing-box")
	}
	if c.InboundTag == "" || strings.ContainsAny(c.InboundTag, "\r\n") {
		return errors.New("inbound_tag is invalid")
	}
	if c.Server == "" || c.RealityPublicKey == "" || c.SNI == "" || c.ShortID == "" {
		return errors.New("server and Reality client fields are required")
	}
	if c.Port < 1 || c.Port > 65535 || c.MixedPort < 1 || c.MixedPort > 65535 {
		return errors.New("port values must be between 1 and 65535")
	}
	if c.Flow != "xtls-rprx-vision" {
		return errors.New("flow must be xtls-rprx-vision")
	}
	host, port, err := net.SplitHostPort(c.StatsAddress)
	if err != nil || port == "" || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("stats_address must use a loopback address")
	}
	return nil
}
