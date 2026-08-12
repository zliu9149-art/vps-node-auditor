package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"vps-node-auditor/internal/config"
	"vps-node-auditor/internal/domain"
)

const testUUID = "11111111-1111-4111-8111-111111111111"

func TestSecureDatabaseFilesUsesOwnerOnlyMode(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "audit.db")
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.WriteFile(candidate, []byte("test"), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	if err := secureDatabaseFiles(path); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
			info, err := os.Stat(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("%s mode = %o", candidate, info.Mode().Perm())
			}
		}
	}
}

func TestCalculateDelta(t *testing.T) {
	t.Parallel()
	previous := collectorState{Upload: 100, Download: 200, CoreBootID: "boot-a"}
	tests := []struct {
		name     string
		previous collectorState
		found    bool
		current  domain.Counters
		bootID   string
		upload   int64
		download int64
		ok       bool
		reset    bool
	}{
		{"baseline", collectorState{}, false, domain.Counters{Upload: 100, Download: 200}, "boot-a", 0, 0, true, false},
		{"normal", previous, true, domain.Counters{Upload: 140, Download: 260}, "boot-a", 40, 60, true, false},
		{"verified restart", previous, true, domain.Counters{Upload: 10, Download: 20}, "boot-b", 10, 20, true, true},
		{"unverified regression", previous, true, domain.Counters{Upload: 90, Download: 190}, "boot-a", 0, 0, false, true},
		{"missing identity regression", previous, true, domain.Counters{Upload: 90, Download: 190}, "", 0, 0, false, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upload, download, ok, reset := calculateDelta(test.previous, test.found, test.current, test.bootID)
			if upload != test.upload || download != test.download || ok != test.ok || reset != test.reset {
				t.Fatalf("calculateDelta() = %d, %d, %v, %v", upload, download, ok, reset)
			}
		})
	}
}

func TestStoreRecordsBaselineDeltasResetAndGap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "audit.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.SyncCredentials(ctx, []config.CredentialConfig{{
		UserName: "alice", CredentialName: "phone", UUID: testUUID, Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}

	t0 := time.Date(2026, 8, 9, 12, 0, 5, 0, time.UTC)
	record := func(at time.Time, boot string, upload, download int64) error {
		return store.RecordSnapshot(ctx, "mock", domain.StatsSnapshot{
			ObservedAt: at,
			CoreBootID: boot,
			Users: map[string]domain.Counters{
				testUUID: {Upload: upload, Download: download},
			},
		})
	}
	if err := record(t0, "boot-a", 100, 200); err != nil {
		t.Fatal(err)
	}
	if err := record(t0.Add(time.Minute), "boot-a", 150, 260); err != nil {
		t.Fatal(err)
	}
	if err := record(t0.Add(2*time.Minute), "boot-b", 10, 20); err != nil {
		t.Fatal(err)
	}
	if err := record(t0.Add(3*time.Minute), "boot-b", 5, 10); err != nil {
		t.Fatal(err)
	}
	if err := record(t0.Add(3*time.Minute), "boot-b", 5, 10); !errors.Is(err, ErrStaleSnapshot) {
		t.Fatalf("duplicate error = %v", err)
	}

	timeline, err := store.Timeline(ctx, "alice", t0.Add(-time.Minute), t0.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(timeline) != 4 {
		t.Fatalf("timeline length = %d", len(timeline))
	}
	if timeline[0].UploadBytes != 0 || timeline[0].DownloadBytes != 0 {
		t.Fatalf("baseline counted prior traffic: %+v", timeline[0])
	}
	if timeline[1].UploadBytes != 50 || timeline[1].DownloadBytes != 60 {
		t.Fatalf("normal delta = %+v", timeline[1])
	}
	if timeline[2].UploadBytes != 10 || timeline[2].DownloadBytes != 20 || !timeline[2].ResetDetected || !timeline[2].CollectionOK {
		t.Fatalf("verified reset = %+v", timeline[2])
	}
	if timeline[3].CollectionOK || !timeline[3].ResetDetected {
		t.Fatalf("unverified regression = %+v", timeline[3])
	}

	top, err := store.Top(ctx, t0.Add(-time.Minute), t0.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 1 || top[0].UploadBytes != 60 || top[0].DownloadBytes != 80 {
		t.Fatalf("top totals = %+v", top)
	}
}

func TestRecordFailureMarksGap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "audit.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.SyncCredentials(ctx, []config.CredentialConfig{{
		UserName: "alice", CredentialName: "phone", UUID: testUUID, Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Minute)
	if err := store.RecordFailure(ctx, "mock", at); err != nil {
		t.Fatal(err)
	}
	timeline, err := store.Timeline(ctx, "alice", at.Add(-time.Minute), at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(timeline) != 1 || timeline[0].CollectionOK {
		t.Fatalf("failure timeline = %+v", timeline)
	}
	health, err := store.Health(ctx, at)
	if err != nil {
		t.Fatal(err)
	}
	if health.RecentFailures != 1 {
		t.Fatalf("recent failures = %d", health.RecentFailures)
	}
}

func TestSameMinuteSnapshotsAccumulateWithoutDuplicateRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "audit.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.SyncCredentials(ctx, []config.CredentialConfig{{
		UserName: "alice", CredentialName: "phone", UUID: testUUID, Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}

	start := time.Date(2026, 8, 9, 12, 0, 5, 0, time.UTC)
	record := func(at time.Time, upload, download int64) error {
		return store.RecordSnapshot(ctx, "mock", domain.StatsSnapshot{
			ObservedAt: at,
			CoreBootID: "boot-a",
			Users: map[string]domain.Counters{
				testUUID: {Upload: upload, Download: download},
			},
		})
	}
	if err := record(start, 100, 200); err != nil {
		t.Fatal(err)
	}
	if err := record(start.Add(20*time.Second), 110, 230); err != nil {
		t.Fatal(err)
	}
	if err := record(start.Add(40*time.Second), 125, 250); err != nil {
		t.Fatal(err)
	}

	timeline, err := store.Timeline(ctx, "alice", start.Add(-time.Minute), start.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(timeline) != 1 {
		t.Fatalf("timeline rows = %d", len(timeline))
	}
	if timeline[0].UploadBytes != 25 || timeline[0].DownloadBytes != 50 {
		t.Fatalf("same-minute aggregate = %+v", timeline[0])
	}
}

func TestTopAggregatesMultipleCredentialsByRegisteredUser(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "audit.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	secondUUID := "22222222-2222-4222-8222-222222222222"
	if err := store.SyncCredentials(ctx, []config.CredentialConfig{
		{UserName: "alice", CredentialName: "phone", UUID: testUUID, Enabled: true},
		{UserName: "alice", CredentialName: "laptop", UUID: secondUUID, Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}

	start := time.Date(2026, 8, 9, 12, 0, 5, 0, time.UTC)
	if err := store.RecordSnapshot(ctx, "mock", domain.StatsSnapshot{
		ObservedAt: start,
		CoreBootID: "boot-a",
		Users: map[string]domain.Counters{
			testUUID:   {Upload: 100, Download: 200},
			secondUUID: {Upload: 300, Download: 400},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSnapshot(ctx, "mock", domain.StatsSnapshot{
		ObservedAt: start.Add(time.Minute),
		CoreBootID: "boot-a",
		Users: map[string]domain.Counters{
			testUUID:   {Upload: 110, Download: 220},
			secondUUID: {Upload: 330, Download: 440},
		},
	}); err != nil {
		t.Fatal(err)
	}

	users, err := store.Top(ctx, start.Add(-time.Minute), start.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].UploadBytes != 40 || users[0].DownloadBytes != 60 || users[0].CredentialName != "" {
		t.Fatalf("user top = %+v", users)
	}
	credentials, err := store.CredentialTop(ctx, start.Add(-time.Minute), start.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(credentials) != 2 {
		t.Fatalf("credential top rows = %d", len(credentials))
	}
}
