package provision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"vps-node-auditor/internal/config"
	"vps-node-auditor/internal/domain"
	"vps-node-auditor/internal/source"
)

type CheckStatus string

const (
	CheckOK      CheckStatus = "ok"
	CheckWarning CheckStatus = "warning"
	CheckError   CheckStatus = "error"
)

type CheckItem struct {
	Name    string      `json:"name"`
	Status  CheckStatus `json:"status"`
	Message string      `json:"message"`
}

type CheckReport struct {
	Status CheckStatus `json:"status"`
	Items  []CheckItem `json:"items"`
}

type ConsistencyChecker struct {
	AuditConfig     config.Config
	ProvisionConfig Config
	Credentials     []domain.Credential
	Summaries       []domain.CredentialSummary
	Health          domain.Health
	HealthError     error
	Runner          Runner
}

func (c ConsistencyChecker) Run(ctx context.Context) CheckReport {
	report := CheckReport{Status: CheckOK}
	add := func(name string, status CheckStatus, message string) {
		report.Items = append(report.Items, CheckItem{Name: name, Status: status, Message: message})
		if status == CheckError || status == CheckWarning && report.Status == CheckOK {
			report.Status = status
		}
	}
	if c.Runner == nil {
		c.Runner = commandRunner{}
	}
	if _, err := c.Runner.Run(ctx, "systemctl", "is-active", "--quiet", c.ProvisionConfig.ServiceName); err != nil {
		add("sing-box service", CheckError, "service is not active")
	} else {
		add("sing-box service", CheckOK, "active")
	}
	if listener, err := c.Runner.Run(ctx, "ss", "-Hln", "sport", "=", ":443"); err != nil || len(strings.TrimSpace(string(listener))) == 0 {
		add("port 443", CheckError, "not listening")
	} else {
		add("port 443", CheckOK, "listening")
	}
	statsUsers, statsErr := source.ListV2RayUsers(ctx, c.ProvisionConfig.StatsAddress, 3*time.Second)
	if statsErr != nil {
		add("Stats API", CheckError, "unavailable")
	} else {
		add("Stats API", CheckOK, "responding")
	}
	if _, err := c.Runner.Run(ctx, "systemctl", "is-active", "--quiet", "node-audit-collector.service"); err != nil {
		add("collector", CheckError, "service is not active")
	} else {
		add("collector", CheckOK, "active")
	}
	checkCollectorFreshness(c.Health, c.HealthError, add)

	coreUsers, coreStats, coreErr := readCoreCredentialSets(c.ProvisionConfig.CoreConfigPath, c.ProvisionConfig.InboundTag)
	if coreErr != nil {
		add("runtime configuration", CheckError, "cannot read active credential sets")
	} else {
		dbUUIDs, dbStats := credentialSets(c.Credentials)
		if equalSet(coreUsers, dbUUIDs) && equalSet(coreStats, dbStats) {
			add("runtime and SQLite", CheckOK, fmt.Sprintf("%d active credentials match", len(dbUUIDs)))
		} else {
			add("runtime and SQLite", CheckError, "active credential sets differ")
		}
		if statsErr == nil {
			unexpected, unobserved := setDifferenceCounts(statsUsers, dbStats)
			switch {
			case unexpected > 0:
				add("Stats users and SQLite", CheckError,
					fmt.Sprintf("%d runtime Stats users have no active SQLite mapping", unexpected))
			case unobserved > 0:
				// QueryStats exposes counters that currently exist; an active user
				// with no traffic may therefore be absent. The strict runtime
				// configuration comparison above already proves that every active
				// SQLite mapping is configured in sing-box.
				add("Stats users and SQLite", CheckWarning,
					fmt.Sprintf("all runtime users map; %d active mappings have no exposed counters yet", unobserved))
			default:
				add("Stats users and SQLite", CheckOK, "mappings match")
			}
		}
	}
	checkYAMLFiles(c.ProvisionConfig, c.Credentials, c.Summaries, add)
	checkFilePermissions(c.AuditConfig.Database.Path, c.ProvisionConfig, add)
	return report
}

func checkCollectorFreshness(health domain.Health, healthErr error,
	add func(string, CheckStatus, string)) {
	if healthErr != nil {
		add("collector freshness", CheckError, "cannot read the latest traffic sample")
	} else if health.LastTrafficSampleAt == nil {
		add("collector freshness", CheckError, "no traffic sample has been recorded")
	} else if !health.TrafficFresh {
		add("collector freshness", CheckError, "latest traffic sample is stale")
	} else {
		add("collector freshness", CheckOK, "latest traffic sample is recent")
	}
}

func readCoreCredentialSets(path, tag string) ([]string, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return readCoreCredentialSetsData(data, tag)
}

func readCoreCredentialSetsData(data []byte, tag string) ([]string, []string, error) {
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, nil, err
	}
	inbounds, ok := root["inbounds"].([]any)
	if !ok {
		return nil, nil, errors.New("missing inbounds")
	}
	var users []string
	matched := false
	for _, raw := range inbounds {
		inbound, _ := raw.(map[string]any)
		if inbound["tag"] != tag {
			continue
		}
		if matched {
			return nil, nil, errors.New("duplicate target inbound")
		}
		matched = true
		rawUsers, ok := inbound["users"].([]any)
		if !ok {
			return nil, nil, errors.New("missing inbound users")
		}
		for _, rawUser := range rawUsers {
			user, _ := rawUser.(map[string]any)
			if uuid, ok := user["uuid"].(string); ok {
				users = append(users, uuid)
			}
		}
	}
	if !matched {
		return nil, nil, errors.New("target inbound is absent")
	}
	experimental, ok := root["experimental"].(map[string]any)
	if !ok {
		return nil, nil, errors.New("missing experimental")
	}
	v2ray, ok := experimental["v2ray_api"].(map[string]any)
	if !ok {
		return nil, nil, errors.New("missing V2Ray API")
	}
	stats, ok := v2ray["stats"].(map[string]any)
	if !ok {
		return nil, nil, errors.New("missing Stats configuration")
	}
	rawStats, ok := stats["users"].([]any)
	if !ok {
		return nil, nil, errors.New("missing stats users")
	}
	var statsUsers []string
	for _, raw := range rawStats {
		if name, ok := raw.(string); ok {
			statsUsers = append(statsUsers, name)
		}
	}
	return users, statsUsers, nil
}

func credentialSets(credentials []domain.Credential) ([]string, []string) {
	uuids := make([]string, 0, len(credentials))
	stats := make([]string, 0, len(credentials))
	for _, credential := range credentials {
		uuids = append(uuids, credential.UUID)
		stats = append(stats, credential.StatsUser)
	}
	return uuids, stats
}

func equalSet(left, right []string) bool {
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	return strings.Join(left, "\x00") == strings.Join(right, "\x00")
}

func setDifferenceCounts(runtimeUsers, databaseUsers []string) (unexpectedRuntime, unobservedDatabase int) {
	runtimeSet := make(map[string]struct{}, len(runtimeUsers))
	databaseSet := make(map[string]struct{}, len(databaseUsers))
	for _, user := range runtimeUsers {
		runtimeSet[user] = struct{}{}
	}
	for _, user := range databaseUsers {
		databaseSet[user] = struct{}{}
	}
	for user := range runtimeSet {
		if _, ok := databaseSet[user]; !ok {
			unexpectedRuntime++
		}
	}
	for user := range databaseSet {
		if _, ok := runtimeSet[user]; !ok {
			unobservedDatabase++
		}
	}
	return unexpectedRuntime, unobservedDatabase
}

func checkYAMLFiles(cfg Config, credentials []domain.Credential, summaries []domain.CredentialSummary,
	add func(string, CheckStatus, string)) {
	active := make(map[string]domain.Credential, len(credentials))
	for _, credential := range credentials {
		active[credential.UserName+"-"+credential.DisplayName+".yaml"] = credential
	}
	missing, mismatch := 0, 0
	for name, credential := range active {
		data, err := os.ReadFile(filepath.Join(cfg.OutputDirectory, name))
		if errors.Is(err, os.ErrNotExist) {
			missing++
			continue
		}
		if err != nil || !strings.Contains(string(data), credential.UUID) ||
			!strings.Contains(string(data), cfg.Server) || !strings.Contains(string(data), cfg.SNI) {
			mismatch++
		}
	}
	if missing > 0 {
		add("active YAML presence", CheckWarning, fmt.Sprintf("%d active credential files are missing", missing))
	} else {
		add("active YAML presence", CheckOK, "all active credential files exist")
	}
	if mismatch > 0 {
		add("active YAML content", CheckError, fmt.Sprintf("%d active credential files do not match", mismatch))
	} else {
		add("active YAML content", CheckOK, "all existing active credential files match")
	}
	residual := 0
	for _, summary := range summaries {
		if summary.Enabled {
			continue
		}
		name := summary.UserName + "-" + summary.CredentialName + ".yaml"
		if _, nowActive := active[name]; nowActive {
			continue
		}
		if _, err := os.Stat(filepath.Join(cfg.OutputDirectory, name)); err == nil {
			residual++
		}
	}
	if residual > 0 {
		add("revoked YAML", CheckWarning, fmt.Sprintf("%d revoked credential files remain", residual))
	} else {
		add("revoked YAML", CheckOK, "none remain")
	}
}

func checkFilePermissions(database string, cfg Config, add func(string, CheckStatus, string)) {
	if runtime.GOOS != "linux" {
		return
	}
	for _, item := range []struct {
		name  string
		path  string
		mode  os.FileMode
		user  string
		group string
	}{
		{"auditor config permissions", "/etc/vps-node-auditor/config.json", 0o640, "root", "node-audit"},
		{"provision config permissions", "/etc/vps-node-auditor/provision.json", 0o600, "root", "root"},
		{"sing-box config permissions", cfg.CoreConfigPath, 0o640, "", ""},
	} {
		info, err := os.Stat(item.path)
		ownerOK := err == nil && (item.user == "" || hasNamedOwner(info, item.user, item.group))
		if err != nil || info.Mode().Perm() != item.mode || !ownerOK {
			expected := fmt.Sprintf("must be %04o", item.mode)
			if item.user != "" {
				expected = fmt.Sprintf("must be %s:%s %04o", item.user, item.group, item.mode)
			}
			add(item.name, CheckError, expected)
		} else {
			add(item.name, CheckOK, "owner and mode match")
		}
	}
	directoryInfo, err := os.Stat(filepath.Dir(database))
	if err != nil || !hasNamedOwner(directoryInfo, "node-audit", "node-audit") || directoryInfo.Mode().Perm() != 0o700 {
		add("database permissions", CheckError, "database directory is unavailable")
		return
	}
	for _, path := range []string{database, database + "-wal", database + "-shm"} {
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || info.Mode().Perm() != 0o600 || !sameFileOwner(info, directoryInfo) {
			add("database permissions", CheckError, "database files must match directory owner and use 0600")
			return
		}
	}
	add("database permissions", CheckOK, "owner and 0600 match")
}
