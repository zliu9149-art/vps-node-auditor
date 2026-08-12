package provision

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"vps-node-auditor/internal/domain"
	"vps-node-auditor/internal/lockfile"
	"vps-node-auditor/internal/source"
)

// CredentialStore is the owner-operation subset of SQLite storage.
// 中文：CredentialStore 是 SQLite 存储中供所有者操作使用的最小接口。
type CredentialStore interface {
	ActiveCredential(context.Context, string, string) (domain.Credential, error)
	ActiveCredentials(context.Context, string) ([]domain.Credential, error)
	AddCredential(context.Context, string, string, string, string, time.Time) error
	RotateCredential(context.Context, int64, string, string, string, time.Time) error
	DisableCredential(context.Context, int64, time.Time) error
	DisableCredentials(context.Context, []int64, time.Time) error
	Backup(context.Context, string) error
}

// Runner executes fixed commands selected by the manager without a shell.
// 中文：Runner 不经 shell 执行管理器选定的固定命令。
type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type commandRunner struct{}

func (commandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Verifier checks the restarted process, listener, Stats endpoint, executable,
// and active configuration readability.
// 中文：Verifier 检查重启后的进程、监听端口、Stats、程序路径和活动配置可读性。
type Verifier interface {
	Verify(context.Context, Config, string) error
}

type productionVerifier struct{ runner Runner }

const (
	productionReadyTimeout = 5 * time.Second
	productionRetryDelay   = 100 * time.Millisecond
)

func (v productionVerifier) Verify(ctx context.Context, cfg Config, priorPID string) error {
	if _, err := v.runner.Run(ctx, "systemctl", "is-active", "--quiet", cfg.ServiceName); err != nil {
		return errors.New("sing-box service is not active after restart")
	}
	pidData, err := v.runner.Run(ctx, "systemctl", "show", "--property", "MainPID", "--value", cfg.ServiceName)
	if err != nil {
		return errors.New("read sing-box PID after restart")
	}
	pid := strings.TrimSpace(string(pidData))
	if pid == "" || pid == "0" || priorPID == "" || pid == priorPID {
		return errors.New("sing-box MainPID did not change during restart")
	}
	readyCtx, cancel := context.WithTimeout(ctx, productionReadyTimeout)
	defer cancel()
	if !waitForProductionReady(readyCtx, productionRetryDelay, func() bool {
		listener, err := v.runner.Run(readyCtx, "ss", "-Hln", "sport", "=", ":443")
		if err != nil || len(strings.TrimSpace(string(listener))) == 0 {
			return false
		}
		return source.ProbeV2RayAPI(readyCtx, cfg.StatsAddress, time.Second) == nil
	}) {
		return errors.New("production port 443 and Stats API did not become ready after restart")
	}
	fragmentData, err := v.runner.Run(ctx, "systemctl", "show", "--property", "FragmentPath", "--value", cfg.ServiceName)
	if err != nil || filepath.Clean(strings.TrimSpace(string(fragmentData))) != filepath.Clean(cfg.CoreUnitPath) {
		return errors.New("sing-box service is not using the expected unit")
	}
	execData, err := v.runner.Run(ctx, "systemctl", "show", "--property", "ExecStart", "--value", cfg.ServiceName)
	if err != nil || !strings.Contains(string(execData), cfg.CoreBinaryPath) || !strings.Contains(string(execData), cfg.CoreConfigPath) {
		return errors.New("sing-box service is not using the expected executable and configuration")
	}
	userData, err := v.runner.Run(ctx, "systemctl", "show", "--property", "User", "--value", cfg.ServiceName)
	if err != nil {
		return errors.New("read sing-box service user after restart")
	}
	serviceUser := strings.TrimSpace(string(userData))
	if serviceUser == "" || serviceUser == "root" {
		if _, err := v.runner.Run(ctx, cfg.CoreBinaryPath, "check", "-c", cfg.CoreConfigPath); err != nil {
			return errors.New("sing-box service identity cannot read the active configuration")
		}
	} else if _, err := v.runner.Run(ctx, "runuser", "-u", serviceUser, "--", cfg.CoreBinaryPath, "check", "-c", cfg.CoreConfigPath); err != nil {
		return errors.New("sing-box service identity cannot read the active configuration")
	}
	return nil
}

func waitForProductionReady(ctx context.Context, retryDelay time.Duration, ready func() bool) bool {
	for {
		if ready() {
			return true
		}
		timer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return false
		case <-timer.C:
		}
	}
}

// Operation identifies one provision state transition.
// 中文：Operation 标识一种 provision 状态转换。
type Operation string

const (
	OperationAdd     Operation = "add"
	OperationRotate  Operation = "rotate"
	OperationDisable Operation = "disable"
	OperationRemove  Operation = "remove-user"
)

// Request describes one owner operation. Every accepted request is applied.
// 中文：Request 描述一次所有者操作；所有被接受的请求都会直接应用。
type Request struct {
	Operation         Operation
	UserName          string
	CredentialName    string
	NewCredentialName string
}

// Result contains safe operator status and protected output locations.
// 中文：Result 包含安全操作状态和受保护的输出位置。
type Result struct {
	Operation  string `json:"operation"`
	UserName   string `json:"user_name"`
	Credential string `json:"credential_name,omitempty"`
	OutputPath string `json:"output_path,omitempty"`
	BackupPath string `json:"backup_path,omitempty"`
}

// Manager coordinates candidate generation, backups, restart verification,
// credential commit, and rollback under one global lock.
// 中文：Manager 在一个全局锁内协调候选生成、备份、重启验证、凭据提交与回滚。
type Manager struct {
	Config        Config
	Store         CredentialStore
	Runner        Runner
	Verifier      Verifier
	VerifyApplied func(context.Context, Config, []byte) error
	Now           func() time.Time
}

// NewManager creates a production manager using direct process execution.
// 中文：NewManager 创建使用直接进程执行的生产管理器。
func NewManager(cfg Config, store CredentialStore) *Manager {
	runner := commandRunner{}
	return &Manager{Config: cfg, Store: store, Runner: runner,
		Verifier: productionVerifier{runner: runner}, VerifyApplied: verifyAppliedCredentialState, Now: time.Now}
}

var labelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Execute validates, backs up, applies, restarts, verifies, and commits one
// state transition. Any failure restores the old configuration and service.
// 中文：Execute 校验、备份、应用、重启、验证并提交一次状态转换；任一步失败都恢复
// 旧配置和服务。
func (m *Manager) Execute(ctx context.Context, request Request) (Result, error) {
	switch request.Operation {
	case OperationAdd, OperationRotate, OperationDisable, OperationRemove:
	default:
		return Result{}, errors.New("unsupported provision operation")
	}
	if !labelPattern.MatchString(request.UserName) {
		return Result{}, errors.New("--user must use 1-64 letters, digits, dot, underscore, or hyphen")
	}
	if request.Operation == OperationAdd && !labelPattern.MatchString(request.CredentialName) {
		return Result{}, errors.New("--credential is invalid")
	}
	if request.Operation == OperationRotate && !labelPattern.MatchString(request.NewCredentialName) {
		return Result{}, errors.New("--new-credential is invalid")
	}
	lock, err := acquireLock(m.Config.LockPath)
	if err != nil {
		return Result{}, err
	}
	defer lock.Close()
	original, err := os.ReadFile(m.Config.CoreConfigPath)
	if err != nil {
		return Result{}, fmt.Errorf("read core configuration: %w", err)
	}
	now := m.Now().UTC()
	var old *domain.Credential
	var oldCredentials []domain.Credential
	if request.Operation == OperationRotate || request.Operation == OperationDisable {
		item, err := m.Store.ActiveCredential(ctx, request.UserName, request.CredentialName)
		if err != nil {
			return Result{}, err
		}
		old = &item
		oldCredentials = []domain.Credential{item}
	}
	if request.Operation == OperationRemove {
		oldCredentials, err = m.Store.ActiveCredentials(ctx, request.UserName)
		if err != nil {
			return Result{}, err
		}
	}
	var addUser *CoreUser
	var removeUsers []CoreUser
	var newUUID, newStatsUser, outputCredential string
	for _, credential := range oldCredentials {
		removeUsers = append(removeUsers, CoreUser{Name: credential.StatsUser, UUID: credential.UUID, Flow: m.Config.Flow})
	}
	if request.Operation == OperationAdd || request.Operation == OperationRotate {
		newUUID, err = randomUUID()
		if err != nil {
			return Result{}, err
		}
		outputCredential = request.CredentialName
		if request.Operation == OperationRotate {
			outputCredential = request.NewCredentialName
		}
		newStatsUser, err = randomStatsUser(request.UserName, outputCredential)
		if err != nil {
			return Result{}, err
		}
		addUser = &CoreUser{Name: newStatsUser, UUID: newUUID, Flow: m.Config.Flow}
	}
	candidate, err := BuildCoreCandidate(original, m.Config.InboundTag, removeUsers, addUser)
	if err != nil {
		return Result{}, err
	}
	result := Result{Operation: string(request.Operation),
		UserName: request.UserName, Credential: outputCredential}
	var clientYAML []byte
	if addUser != nil {
		clientYAML, err = BuildMihomoYAML(m.Config, request.UserName+"-"+outputCredential, newUUID)
		if err != nil {
			return Result{}, err
		}
	}
	if err := os.MkdirAll(m.Config.BackupDirectory, 0o700); err != nil {
		return Result{}, fmt.Errorf("create rollback directory: %w", err)
	}
	backupPath, err := os.MkdirTemp(m.Config.BackupDirectory,
		now.Format("20060102T150405Z")+"-"+string(request.Operation)+"-*")
	if err != nil {
		return Result{}, fmt.Errorf("create unique rollback directory: %w", err)
	}
	if err := os.Chmod(backupPath, 0o700); err != nil {
		return Result{}, fmt.Errorf("restrict rollback directory: %w", err)
	}
	for _, item := range []struct{ source, name string }{
		{m.Config.CoreConfigPath, "sing-box-config.json"},
		{m.Config.CoreBinaryPath, "sing-box"},
		{m.Config.CoreUnitPath, "sing-box.service"},
	} {
		if err := copyPrivateFile(item.source, filepath.Join(backupPath, item.name)); err != nil {
			return Result{}, err
		}
	}
	if err := m.Store.Backup(ctx, filepath.Join(backupPath, "audit.db")); err != nil {
		return Result{}, err
	}
	result.BackupPath = backupPath

	type stagedYAML struct {
		original, staged string
		data             []byte
		mode             os.FileMode
	}
	var stagedYAMLs []stagedYAML
	restoreStagedYAMLs := func() error {
		var restoreErrors []error
		for _, item := range stagedYAMLs {
			if _, err := os.Stat(item.staged); err == nil {
				if err := os.Rename(item.staged, item.original); err != nil {
					restoreErrors = append(restoreErrors, fmt.Errorf("restore revoked client YAML: %w", err))
				}
			} else if errors.Is(err, os.ErrNotExist) {
				if err := atomicWrite(item.original, item.data, item.mode); err != nil {
					restoreErrors = append(restoreErrors, fmt.Errorf("recreate revoked client YAML: %w", err))
				}
			} else {
				restoreErrors = append(restoreErrors, fmt.Errorf("inspect staged client YAML: %w", err))
			}
		}
		return errors.Join(restoreErrors...)
	}

	coreInfo, err := os.Stat(m.Config.CoreConfigPath)
	if err != nil {
		return Result{}, fmt.Errorf("inspect core configuration: %w", err)
	}
	candidatePath, err := writeCandidate(m.Config.CoreConfigPath, candidate, coreInfo.Mode().Perm())
	if err != nil {
		return Result{}, err
	}
	defer os.Remove(candidatePath)
	if _, err := m.Runner.Run(ctx, m.Config.CoreBinaryPath, "check", "-c", candidatePath); err != nil {
		return Result{}, errors.New("sing-box rejected the candidate configuration")
	}
	pidData, err := m.Runner.Run(ctx, "systemctl", "show", "--property", "MainPID", "--value", m.Config.ServiceName)
	if err != nil {
		return Result{}, errors.New("read sing-box PID before restart")
	}
	priorPID := strings.TrimSpace(string(pidData))
	if priorPID == "" || priorPID == "0" {
		return Result{}, errors.New("sing-box has no active PID before restart")
	}

	var pendingYAML, finalYAML string
	if len(clientYAML) > 0 {
		if err := os.MkdirAll(m.Config.OutputDirectory, 0o700); err != nil {
			return Result{}, fmt.Errorf("create YAML output directory: %w", err)
		}
		if err := os.Chmod(m.Config.OutputDirectory, 0o700); err != nil {
			return Result{}, fmt.Errorf("restrict YAML output directory: %w", err)
		}
		finalYAML = filepath.Join(m.Config.OutputDirectory, request.UserName+"-"+outputCredential+".yaml")
		pendingYAML = finalYAML + ".pending"
		if _, err := os.Stat(finalYAML); err == nil {
			return Result{}, errors.New("client YAML already exists; choose a new credential name")
		} else if !errors.Is(err, os.ErrNotExist) {
			return Result{}, fmt.Errorf("inspect client YAML destination: %w", err)
		}
		if err := os.WriteFile(pendingYAML, clientYAML, 0o600); err != nil {
			return Result{}, fmt.Errorf("write pending YAML: %w", err)
		}
		defer os.Remove(pendingYAML)
	}
	for _, credential := range oldCredentials {
		originalPath := filepath.Join(m.Config.OutputDirectory,
			request.UserName+"-"+credential.DisplayName+".yaml")
		info, err := os.Stat(originalPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return Result{}, fmt.Errorf("inspect revoked client YAML: %w", err)
		}
		data, err := os.ReadFile(originalPath)
		if err != nil {
			return Result{}, fmt.Errorf("read revoked client YAML: %w", err)
		}
		stagedPath := filepath.Join(backupPath, "revoked-"+credential.DisplayName+".yaml")
		if err := os.Rename(originalPath, stagedPath); err != nil {
			return Result{}, errors.Join(fmt.Errorf("stage revoked client YAML: %w", err), restoreStagedYAMLs())
		}
		stagedYAMLs = append(stagedYAMLs, stagedYAML{original: originalPath, staged: stagedPath,
			data: data, mode: info.Mode().Perm()})
	}
	if err := replaceFile(candidatePath, m.Config.CoreConfigPath, coreInfo.Mode().Perm()); err != nil {
		return Result{}, errors.Join(err, restoreStagedYAMLs())
	}
	rollback := func(cause error) error {
		restoreErr := atomicWrite(m.Config.CoreConfigPath, original, coreInfo.Mode().Perm())
		_, restartErr := m.Runner.Run(ctx, "systemctl", "restart", m.Config.ServiceName)
		verifyErr := error(nil)
		verifyStateErr := error(nil)
		if restartErr == nil {
			verifyErr = m.Verifier.Verify(ctx, m.Config, "0")
			if verifyErr == nil && m.VerifyApplied != nil {
				verifyStateErr = m.VerifyApplied(ctx, m.Config, original)
			}
		}
		removeYAMLErr := error(nil)
		if finalYAML != "" {
			if err := os.Remove(finalYAML); err != nil && !errors.Is(err, os.ErrNotExist) {
				removeYAMLErr = fmt.Errorf("remove uncommitted client YAML: %w", err)
			}
		}
		return errors.Join(cause, restoreErr, restartErr, verifyErr, verifyStateErr,
			removeYAMLErr, restoreStagedYAMLs())
	}
	if _, err := m.Runner.Run(ctx, "systemctl", "restart", m.Config.ServiceName); err != nil {
		return Result{}, rollback(errors.New("sing-box restart failed"))
	}
	if err := m.Verifier.Verify(ctx, m.Config, priorPID); err != nil {
		return Result{}, rollback(err)
	}
	if m.VerifyApplied != nil {
		if err := m.VerifyApplied(ctx, m.Config, candidate); err != nil {
			return Result{}, rollback(err)
		}
	}
	if pendingYAML != "" {
		if err := replaceFile(pendingYAML, finalYAML, 0o600); err != nil {
			return Result{}, rollback(err)
		}
		result.OutputPath = finalYAML
	}
	for _, item := range stagedYAMLs {
		if err := os.Remove(item.staged); err != nil {
			return Result{}, rollback(fmt.Errorf("delete revoked client YAML: %w", err))
		}
	}
	switch request.Operation {
	case OperationAdd:
		err = m.Store.AddCredential(ctx, request.UserName, request.CredentialName, newUUID, newStatsUser, now)
	case OperationRotate:
		err = m.Store.RotateCredential(ctx, old.ID, request.NewCredentialName, newUUID, newStatsUser, now)
	case OperationDisable:
		err = m.Store.DisableCredential(ctx, old.ID, now)
	case OperationRemove:
		ids := make([]int64, 0, len(oldCredentials))
		for _, credential := range oldCredentials {
			ids = append(ids, credential.ID)
		}
		err = m.Store.DisableCredentials(ctx, ids, now)
	default:
		err = errors.New("unsupported provision operation")
	}
	if err != nil {
		return Result{}, rollback(fmt.Errorf("commit credential transaction: %w", err))
	}
	return result, nil
}

func verifyAppliedCredentialState(ctx context.Context, cfg Config, candidate []byte) error {
	expectedUsers, expectedStats, err := readCoreCredentialSetsData(candidate, cfg.InboundTag)
	if err != nil {
		return errors.New("read expected credential state")
	}
	actualUsers, actualStats, err := readCoreCredentialSets(cfg.CoreConfigPath, cfg.InboundTag)
	if err != nil || !equalSet(expectedUsers, actualUsers) || !equalSet(expectedStats, actualStats) {
		return errors.New("active configuration does not match the verified candidate")
	}
	runtimeStats, err := source.ListV2RayUsers(ctx, cfg.StatsAddress, 3*time.Second)
	if err != nil {
		return errors.New("runtime Stats users are unavailable after applying the configuration")
	}
	unexpectedRuntime, _ := setDifferenceCounts(runtimeStats, expectedStats)
	if unexpectedRuntime > 0 {
		return errors.New("runtime Stats exposes users outside the active configuration")
	}
	return nil
}

func acquireLock(path string) (*lockfile.Lock, error) {
	lock, busy, err := lockfile.TryAcquire(path)
	if err != nil {
		return nil, fmt.Errorf("acquire global configuration lock: %w", err)
	}
	if busy {
		return nil, errors.New("another configuration operation holds the global lock")
	}
	return lock, nil
}

func randomUUID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", errors.New("generate credential UUID")
	}
	data[6] = (data[6] & 0x0f) | 0x40
	data[8] = (data[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(data)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}

func randomStatsUser(userName, credentialName string) (string, error) {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return "", errors.New("generate Stats user name")
	}
	return userName + "-" + credentialName + "-stats-" + hex.EncodeToString(suffix), nil
}

func writeCandidate(target string, data []byte, mode os.FileMode) (name string, err error) {
	reference, err := os.Stat(target)
	if err != nil {
		return "", fmt.Errorf("inspect candidate ownership reference: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(target), ".node-provision-candidate-*")
	if err != nil {
		return "", fmt.Errorf("create candidate file: %w", err)
	}
	name = file.Name()
	defer func() {
		file.Close()
		if err != nil {
			os.Remove(name)
		}
	}()
	if err = matchFileOwnership(name, reference); err != nil {
		return "", fmt.Errorf("preserve candidate ownership: %w", err)
	}
	if err = file.Chmod(mode); err != nil {
		return "", fmt.Errorf("set candidate permissions: %w", err)
	}
	if _, err = file.Write(data); err != nil {
		return "", fmt.Errorf("write candidate file: %w", err)
	}
	if err = file.Sync(); err != nil {
		return "", fmt.Errorf("sync candidate file: %w", err)
	}
	if err = file.Close(); err != nil {
		return "", fmt.Errorf("close candidate file: %w", err)
	}
	return name, nil
}

func replaceFile(source, destination string, mode os.FileMode) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read replacement file: %w", err)
	}
	return atomicWrite(destination, data, mode)
}

func atomicWrite(destination string, data []byte, mode os.FileMode) error {
	var reference os.FileInfo
	info, err := os.Stat(destination)
	if err == nil {
		reference = info
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect replacement ownership reference: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".node-provision-replace-*")
	if err != nil {
		return fmt.Errorf("create atomic replacement: %w", err)
	}
	name := temporary.Name()
	defer os.Remove(name)
	if reference != nil {
		if err := matchFileOwnership(name, reference); err != nil {
			temporary.Close()
			return fmt.Errorf("preserve replacement ownership: %w", err)
		}
	}
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return fmt.Errorf("set replacement permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write atomic replacement: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync atomic replacement: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close atomic replacement: %w", err)
	}
	if err := os.Rename(name, destination); err != nil {
		return fmt.Errorf("activate atomic replacement: %w", err)
	}
	return nil
}

func copyPrivateFile(source, destination string) error {
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("inspect rollback source: %w", err)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read rollback source: %w", err)
	}
	mode := info.Mode().Perm() & 0o700
	if mode == 0 {
		mode = 0o600
	}
	if err := os.WriteFile(destination, data, mode); err != nil {
		return fmt.Errorf("write rollback file: %w", err)
	}
	if err := os.Chmod(destination, mode); err != nil {
		return fmt.Errorf("restrict rollback file: %w", err)
	}
	return nil
}
