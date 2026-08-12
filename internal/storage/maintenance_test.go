package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vps-node-auditor/internal/config"
	"vps-node-auditor/internal/domain"
)

func TestMaintenanceRollsUpCleansAndBacksUp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	directory := t.TempDir()
	store, err := Open(filepath.Join(directory, "audit.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	credentials := make([]config.CredentialConfig, 5)
	uuids := []string{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
		"44444444-4444-4444-8444-444444444444",
		"55555555-5555-4555-8555-555555555555",
	}
	for i := range credentials {
		credentials[i] = config.CredentialConfig{
			UserName: "user-" + string(rune('a'+i)), CredentialName: "device-" + string(rune('a'+i)),
			UUID: uuids[i], StatsUser: "stats-" + string(rune('a'+i)), Enabled: true,
		}
	}
	if err := store.SyncCredentials(ctx, credentials); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	users := make(map[string]domain.Counters)
	for i, uuid := range uuids {
		users[uuid] = domain.Counters{Upload: int64(100 + i), Download: int64(200 + i)}
	}
	if err := store.RecordSnapshot(ctx, "mock", domain.StatsSnapshot{ObservedAt: start, CoreBootID: "boot", Users: users}); err != nil {
		t.Fatal(err)
	}
	for i, uuid := range uuids {
		users[uuid] = domain.Counters{Upload: int64(110 + i), Download: int64(220 + i)}
	}
	if err := store.RecordSnapshot(ctx, "mock", domain.StatsSnapshot{ObservedAt: start.Add(time.Hour), CoreBootID: "boot", Users: users}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordHostSnapshot(ctx, "linux-host", domain.HostSnapshot{
		ObservedAt: start, HostBootID: "boot", CPUValid: true, CPUTotal: 100, CPUIdle: 80,
		MemoryValid: true, NetworkValid: true, TCPValid: true, ConnectionsValid: true,
		ProxyValid: true, ProxyServiceOK: true,
	}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Display:   config.DisplayConfig{Timezone: "Asia/Hong_Kong"},
		Provider:  config.ProviderConfig{BillingTimezone: "Asia/Hong_Kong"},
		Retention: config.RetentionConfig{MinuteDays: 1, EventDays: 1, RollupDays: 400, BackupCount: 14},
	}
	result, err := store.Maintain(ctx, cfg, start.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if result.TrafficRollups == 0 || result.SystemRollups == 0 || result.TrafficDeleted == 0 {
		t.Fatalf("unexpected maintenance result: %+v", result)
	}
	items, err := store.TimelineBucket(ctx, "user-a", start, start.Add(2*time.Hour), domain.BucketHour, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[1].UploadBytes != 10 || items[1].DownloadBytes != 20 {
		t.Fatalf("hour rollups = %+v", items)
	}
	backup := filepath.Join(directory, "backups", "audit-test.db")
	if err := store.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(backup); err != nil || info.Size() == 0 {
		t.Fatalf("backup was not created: %v, %v", info, err)
	}
}

func TestAlertsDeduplicateQuotaAndConsecutiveFailures(t *testing.T) {
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
		UserName: "alice", CredentialName: "phone", UUID: testUUID, StatsUser: "alice-stats", Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	if err := store.RecordSnapshot(ctx, "mock", domain.StatsSnapshot{ObservedAt: start, CoreBootID: "boot", Users: map[string]domain.Counters{testUUID: {}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSnapshot(ctx, "mock", domain.StatsSnapshot{ObservedAt: start.Add(time.Minute), CoreBootID: "boot", Users: map[string]domain.Counters{testUUID: {Upload: 60}}}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Display:  config.DisplayConfig{Timezone: "UTC"},
		Provider: config.ProviderConfig{MonthlyQuotaBytes: 100, CycleStartDay: 9, BillingTimezone: "UTC", TrafficDirection: "both"},
		Alerts:   config.AlertConfig{QuotaPercentages: []float64{50}, TrafficDifferencePct: 1000, ConsecutiveFailures: 3},
	}
	for i := 0; i < 2; i++ {
		if err := store.EvaluateAlerts(ctx, cfg, start.Add(2*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	var alerts int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE category='alert'").Scan(&alerts); err != nil {
		t.Fatal(err)
	}
	if alerts != 1 {
		t.Fatalf("quota alert count = %d", alerts)
	}
	for i := 3; i <= 5; i++ {
		if err := store.RecordFailure(ctx, "mock", start.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.EvaluateAlerts(ctx, cfg, start.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE category='alert'").Scan(&alerts); err != nil {
		t.Fatal(err)
	}
	if alerts != 2 {
		t.Fatalf("combined alert count = %d", alerts)
	}
}

func TestMigrateUpgradesLegacyUnversionedSchema(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "audit.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.ExecContext(ctx, migrations[0].SQL); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO users(display_name,created_at) VALUES('alice',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO credentials(user_id,display_name,uuid,stats_user,enabled,created_at)
		SELECT id,'phone',?,'alice-stats',1,1 FROM users`, testUUID); err != nil {
		t.Fatalf("stats_user migration missing: %v", err)
	}
}

func TestMigrationThreePreservesHistoryAndAllowsOnlyOneActiveName(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "audit.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, item := range migrations[:2] {
		if _, err := store.db.ExecContext(ctx, item.SQL); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TABLE schema_migrations (
		version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO schema_migrations(version, applied_at) VALUES(1,1),(2,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO users(display_name,created_at) VALUES('alice',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO credentials(
		user_id,display_name,uuid,stats_user,enabled,created_at,revoked_at)
		SELECT id,'phone',?,'stats-old',0,1,2 FROM users`, testUUID); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.AddCredential(ctx, "alice", "phone",
		"22222222-2222-4222-8222-222222222222", "stats-new", time.Unix(3, 0)); err != nil {
		t.Fatal(err)
	}
	if err := store.AddCredential(ctx, "alice", "phone",
		"33333333-3333-4333-8333-333333333333", "stats-extra", time.Unix(4, 0)); err == nil ||
		!strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("second active name error = %v", err)
	}
}
