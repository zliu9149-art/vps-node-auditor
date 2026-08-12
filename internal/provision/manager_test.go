package provision

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"vps-node-auditor/internal/domain"
)

type fakeStore struct {
	active       domain.Credential
	activeSet    []domain.Credential
	addCalls     int
	rotateCalls  int
	disableCalls int
	commitErr    error
}

func (s *fakeStore) ActiveCredential(context.Context, string, string) (domain.Credential, error) {
	if s.active.ID == 0 {
		return domain.Credential{}, errors.New("no active credential")
	}
	return s.active, nil
}
func (s *fakeStore) ActiveCredentials(context.Context, string) ([]domain.Credential, error) {
	if len(s.activeSet) > 0 {
		return append([]domain.Credential(nil), s.activeSet...), nil
	}
	if s.active.ID == 0 {
		return nil, errors.New("no active credentials")
	}
	return []domain.Credential{s.active}, nil
}

type scriptedRunner struct {
	responses map[string]struct {
		output []byte
		err    error
	}
	calls []string
}

func (r *scriptedRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, key)
	response, ok := r.responses[key]
	if !ok {
		return nil, fmt.Errorf("unexpected command: %s", key)
	}
	return response.output, response.err
}
func (s *fakeStore) AddCredential(context.Context, string, string, string, string, time.Time) error {
	s.addCalls++
	return s.commitErr
}
func (s *fakeStore) RotateCredential(context.Context, int64, string, string, string, time.Time) error {
	s.rotateCalls++
	return s.commitErr
}
func (s *fakeStore) DisableCredential(context.Context, int64, time.Time) error {
	s.disableCalls++
	return s.commitErr
}
func (s *fakeStore) DisableCredentials(_ context.Context, ids []int64, _ time.Time) error {
	s.disableCalls += len(ids)
	return s.commitErr
}
func (s *fakeStore) Backup(_ context.Context, destination string) error {
	return os.WriteFile(destination, []byte("database-backup"), 0o600)
}

type fakeRunner struct {
	checkErr   error
	restartErr error
	restarts   int
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if strings.HasSuffix(name, "sing-box") && len(args) > 0 && args[0] == "check" {
		return nil, r.checkErr
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "show" {
		return []byte("123\n"), nil
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "restart" {
		r.restarts++
		if r.restarts == 1 {
			return nil, r.restartErr
		}
	}
	return []byte("ok\n"), nil
}

type fakeVerifier struct {
	err   error
	calls int
}

type candidateAwareVerifier struct {
	path           string
	originalMarker string
	applyCalls     int
	rollbackCalls  int
}

func (v *candidateAwareVerifier) Verify(_ context.Context, _ Config, priorPID string) error {
	data, err := os.ReadFile(v.path)
	if err != nil {
		return err
	}
	if priorPID == "0" {
		v.rollbackCalls++
		if !strings.Contains(string(data), v.originalMarker) {
			return errors.New("rollback runtime did not restore original credentials")
		}
	} else {
		v.applyCalls++
	}
	return nil
}

func (v *fakeVerifier) Verify(context.Context, Config, string) error {
	v.calls++
	if v.calls == 1 {
		return v.err
	}
	return nil
}

func testConfig(directory string) Config {
	return Config{
		CoreConfigPath: filepath.Join(directory, "config.json"),
		CoreBinaryPath: filepath.Join(directory, "sing-box"),
		CoreUnitPath:   filepath.Join(directory, "sing-box.service"),
		ServiceName:    "sing-box", InboundTag: "proxy-in",
		OutputDirectory: filepath.Join(directory, "clients"),
		BackupDirectory: filepath.Join(directory, "backups"),
		LockPath:        filepath.Join(directory, "config.lock"),
		Server:          "203.0.113.10", Port: 443, RealityPublicKey: "public",
		SNI: "www.example.com", ShortID: "0123456789abcdef",
		Flow: "xtls-rprx-vision", MixedPort: 7890,
		StatsAddress: "127.0.0.1:10085",
	}
}

func testManager(t *testing.T, store *fakeStore, runner *fakeRunner, verifier *fakeVerifier) (*Manager, []byte) {
	t.Helper()
	directory := t.TempDir()
	cfg := testConfig(directory)
	original := []byte(coreFixture)
	for path, data := range map[string][]byte{
		cfg.CoreConfigPath: original, cfg.CoreBinaryPath: []byte("binary"), cfg.CoreUnitPath: []byte("unit"),
	} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &Manager{Config: cfg, Store: store, Runner: runner, Verifier: verifier,
		VerifyApplied: func(context.Context, Config, []byte) error { return nil },
		Now:           func() time.Time { return time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC) }}, original
}

func TestCandidateCheckFailureLeavesProductionUntouched(t *testing.T) {
	store := &fakeStore{}
	runner := &fakeRunner{checkErr: errors.New("invalid")}
	verifier := &fakeVerifier{}
	manager, original := testManager(t, store, runner, verifier)
	_, err := manager.Execute(context.Background(), Request{
		Operation: OperationAdd, UserName: "alice", CredentialName: "phone",
	})
	if err == nil {
		t.Fatal("expected syntax check failure")
	}
	current, _ := os.ReadFile(manager.Config.CoreConfigPath)
	if string(current) != string(original) || store.addCalls != 0 || runner.restarts != 0 || verifier.calls != 0 {
		t.Fatal("syntax failure modified production state")
	}
}

func TestRestartVerifierAndDatabaseFailuresRollBack(t *testing.T) {
	for _, test := range []struct {
		name       string
		restartErr error
		verifyErr  error
		commitErr  error
	}{
		{"restart", errors.New("restart failed"), nil, nil},
		{"verify", nil, errors.New("verification failed"), nil},
		{"database", nil, nil, errors.New("database failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{commitErr: test.commitErr}
			runner := &fakeRunner{restartErr: test.restartErr}
			verifier := &fakeVerifier{err: test.verifyErr}
			manager, original := testManager(t, store, runner, verifier)
			_, err := manager.Execute(context.Background(), Request{
				Operation: OperationAdd, UserName: "alice", CredentialName: "phone",
			})
			if err == nil {
				t.Fatal("expected operation failure")
			}
			current, _ := os.ReadFile(manager.Config.CoreConfigPath)
			if string(current) != string(original) {
				t.Fatal("core configuration was not rolled back")
			}
			if runner.restarts != 2 {
				t.Fatalf("restart count = %d, want apply plus recovery", runner.restarts)
			}
			if _, err := os.Stat(filepath.Join(manager.Config.OutputDirectory, "alice-phone.yaml")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed operation retained client YAML")
			}
		})
	}
}

func TestDatabaseFailureVerifiesOriginalRuntimeCredentials(t *testing.T) {
	store := &fakeStore{commitErr: errors.New("database failed")}
	runner := &fakeRunner{}
	manager, original := testManager(t, store, runner, &fakeVerifier{})
	verifier := &candidateAwareVerifier{path: manager.Config.CoreConfigPath,
		originalMarker: "old-stats"}
	manager.Verifier = verifier
	_, err := manager.Execute(context.Background(), Request{
		Operation: OperationAdd, UserName: "alice", CredentialName: "phone",
	})
	if err == nil {
		t.Fatal("expected database failure")
	}
	current, readErr := os.ReadFile(manager.Config.CoreConfigPath)
	if readErr != nil || string(current) != string(original) {
		t.Fatalf("original configuration was not restored: %v", readErr)
	}
	if verifier.applyCalls != 1 || verifier.rollbackCalls != 1 {
		t.Fatalf("runtime verification calls: apply=%d rollback=%d", verifier.applyCalls, verifier.rollbackCalls)
	}
}

func TestSuccessWritesPrivateYAMLCommitsAndUsesUniqueBackups(t *testing.T) {
	var backups []string
	for range 2 {
		store := &fakeStore{}
		runner := &fakeRunner{}
		verifier := &fakeVerifier{}
		manager, _ := testManager(t, store, runner, verifier)
		result, err := manager.Execute(context.Background(), Request{
			Operation: OperationAdd, UserName: "alice", CredentialName: "phone",
		})
		if err != nil {
			t.Fatal(err)
		}
		if store.addCalls != 1 || result.OutputPath == "" || runner.restarts != 1 || verifier.calls != 1 {
			t.Fatalf("successful result = %+v, add=%d restart=%d verify=%d", result, store.addCalls, runner.restarts, verifier.calls)
		}
		info, err := os.Stat(result.OutputPath)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("YAML permissions = %o", info.Mode().Perm())
		}
		backups = append(backups, filepath.Base(result.BackupPath))
	}
	if backups[0] == backups[1] {
		t.Fatalf("backup names collided: %q", backups[0])
	}
}

func TestRemoveUserDeletesYAMLAndCommitsOnce(t *testing.T) {
	store := &fakeStore{active: domain.Credential{ID: 7, UserName: "alice", DisplayName: "phone",
		UUID: "11111111-1111-4111-8111-111111111111", StatsUser: "old-stats", Enabled: true}}
	runner := &fakeRunner{}
	verifier := &fakeVerifier{}
	manager, _ := testManager(t, store, runner, verifier)
	if err := os.MkdirAll(manager.Config.OutputDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	yamlPath := filepath.Join(manager.Config.OutputDirectory, "alice-phone.yaml")
	if err := os.WriteFile(yamlPath, []byte("private yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Execute(context.Background(), Request{Operation: OperationRemove, UserName: "alice"}); err != nil {
		t.Fatal(err)
	}
	if store.disableCalls != 1 || runner.restarts != 1 {
		t.Fatalf("disable=%d restarts=%d", store.disableCalls, runner.restarts)
	}
	if _, err := os.Stat(yamlPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("revoked YAML remains")
	}
	core, err := os.ReadFile(manager.Config.CoreConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(core), "old-stats") || strings.Contains(string(core), store.active.UUID) {
		t.Fatal("revoked credential remains in core")
	}
}

func TestRemoveUserRevokesMultipleDevicesWithOneRestartAndCommit(t *testing.T) {
	credentials := []domain.Credential{
		{ID: 7, UserName: "alice", DisplayName: "phone", UUID: "11111111-1111-4111-8111-111111111111", StatsUser: "old-stats", Enabled: true},
		{ID: 8, UserName: "alice", DisplayName: "laptop", UUID: "22222222-2222-4222-8222-222222222222", StatsUser: "second-stats", Enabled: true},
	}
	store := &fakeStore{activeSet: credentials}
	runner := &fakeRunner{}
	verifier := &fakeVerifier{}
	manager, original := testManager(t, store, runner, verifier)
	coreWithBoth, err := BuildCoreCandidate(original, manager.Config.InboundTag, nil,
		&CoreUser{Name: credentials[1].StatsUser, UUID: credentials[1].UUID, Flow: manager.Config.Flow})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.Config.CoreConfigPath, coreWithBoth, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(manager.Config.OutputDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, credential := range credentials {
		path := filepath.Join(manager.Config.OutputDirectory, "alice-"+credential.DisplayName+".yaml")
		if err := os.WriteFile(path, []byte("private yaml"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := manager.Execute(context.Background(), Request{Operation: OperationRemove, UserName: "alice"}); err != nil {
		t.Fatal(err)
	}
	if store.disableCalls != 2 || runner.restarts != 1 || verifier.calls != 1 {
		t.Fatalf("disabled=%d restarts=%d verifies=%d", store.disableCalls, runner.restarts, verifier.calls)
	}
	for _, credential := range credentials {
		path := filepath.Join(manager.Config.OutputDirectory, "alice-"+credential.DisplayName+".yaml")
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("revoked YAML remains: %s", path)
		}
	}
	core, err := os.ReadFile(manager.Config.CoreConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, credential := range credentials {
		if strings.Contains(string(core), credential.UUID) || strings.Contains(string(core), credential.StatsUser) {
			t.Fatalf("revoked device remains in core: %s", credential.DisplayName)
		}
	}
}

func TestProductionVerifierRequiresChangedMainPID(t *testing.T) {
	base := map[string]struct {
		output []byte
		err    error
	}{
		"systemctl is-active --quiet sing-box":               {output: []byte("active\n")},
		"systemctl show --property MainPID --value sing-box": {output: []byte("123\n")},
	}
	for _, test := range []struct {
		name, prior string
	}{
		{name: "unchanged", prior: "123"},
		{name: "missing prior", prior: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &scriptedRunner{responses: base}
			err := (productionVerifier{runner: runner}).Verify(context.Background(), testConfig(t.TempDir()), test.prior)
			if err == nil || !strings.Contains(err.Error(), "MainPID did not change") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestWaitForProductionReadyRetriesAndHonorsTimeout(t *testing.T) {
	attempts := 0
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !waitForProductionReady(ctx, 0, func() bool {
		attempts++
		return attempts == 3
	}) {
		t.Fatal("readiness did not recover")
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}

	timedOut, stop := context.WithCancel(context.Background())
	stop()
	if waitForProductionReady(timedOut, 0, func() bool { return false }) {
		t.Fatal("cancelled readiness check succeeded")
	}
}

func TestRuntimeStatsMayOmitConfiguredUsersWithoutCounters(t *testing.T) {
	for _, test := range []struct {
		name              string
		runtime, expected []string
		wantUnexpected    int
	}{
		{"exact", []string{"active", "new"}, []string{"active", "new"}, 0},
		{"new user has no counter", []string{"active"}, []string{"active", "new"}, 0},
		{"stale runtime user", []string{"active", "revoked"}, []string{"active", "new"}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			unexpected, _ := setDifferenceCounts(test.runtime, test.expected)
			if unexpected != test.wantUnexpected {
				t.Fatalf("unexpected runtime users = %d, want %d", unexpected, test.wantUnexpected)
			}
		})
	}
}

func TestRemoveUserDatabaseFailureRestoresYAMLAndCore(t *testing.T) {
	store := &fakeStore{active: domain.Credential{ID: 7, UserName: "alice", DisplayName: "phone",
		UUID: "11111111-1111-4111-8111-111111111111", StatsUser: "old-stats", Enabled: true},
		commitErr: errors.New("database failed")}
	runner := &fakeRunner{}
	verifier := &fakeVerifier{}
	manager, original := testManager(t, store, runner, verifier)
	if err := os.MkdirAll(manager.Config.OutputDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	yamlPath := filepath.Join(manager.Config.OutputDirectory, "alice-phone.yaml")
	if err := os.WriteFile(yamlPath, []byte("private yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Execute(context.Background(), Request{Operation: OperationRemove, UserName: "alice"}); err == nil {
		t.Fatal("expected database failure")
	}
	core, _ := os.ReadFile(manager.Config.CoreConfigPath)
	yaml, yamlErr := os.ReadFile(yamlPath)
	if string(core) != string(original) || yamlErr != nil || string(yaml) != "private yaml" || runner.restarts != 2 {
		t.Fatalf("rollback core=%t yaml=%q err=%v restarts=%d", string(core) == string(original), yaml, yamlErr, runner.restarts)
	}
}
