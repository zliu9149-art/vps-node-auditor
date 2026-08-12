package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"vps-node-auditor/internal/domain"
)

func TestHostCountersBaselineRestartAndPartialFailure(t *testing.T) {
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
	start := time.Date(2026, 8, 9, 12, 0, 5, 0, time.UTC)
	snapshot := func(at time.Time, boot string, cpuTotal, cpuIdle, rx, tx, retrans int64, missing ...string) domain.HostSnapshot {
		return domain.HostSnapshot{
			ObservedAt: at, HostBootID: boot, InterfaceName: "eth0",
			CPUTotal: cpuTotal, CPUIdle: cpuIdle, CPUValid: true,
			MemoryUsedBytes: 100, MemoryValid: true,
			NetworkRXTotal: rx, NetworkTXTotal: tx, NetworkValid: true,
			TCPRetransTotal: retrans, TCPResetsTotal: 2, TCPValid: true,
			ConnectionsValid: true, ProxyValid: true, ProxyServiceOK: true,
			MissingSources: missing,
			VnStat:         &domain.VnStatSnapshot{InterfaceName: "eth0", RXTotal: rx, TXTotal: tx, CollectionOK: true},
		}
	}
	if err := store.RecordHostSnapshot(ctx, "linux-host", snapshot(start, "boot-a", 100, 80, 1000, 2000, 10)); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordHostSnapshot(ctx, "linux-host", snapshot(start.Add(time.Minute), "boot-a", 200, 130, 1300, 2400, 14)); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordHostSnapshot(ctx, "linux-host", snapshot(start.Add(2*time.Minute), "boot-b", 20, 15, 100, 200, 1)); err != nil {
		t.Fatal(err)
	}
	partial := snapshot(start.Add(3*time.Minute), "boot-b", 30, 20, 120, 230, 2, "vnstat")
	partial.VnStat = nil
	if err := store.RecordHostSnapshot(ctx, "linux-host", partial); err != nil {
		t.Fatal(err)
	}

	items, err := store.SystemSamples(ctx, start.Add(-time.Minute), start.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("system sample count = %d", len(items))
	}
	if items[0].NetworkRXBytes != 0 || items[0].TCPRetransmits != 0 {
		t.Fatalf("first baseline counted totals: %+v", items[0])
	}
	if items[1].NetworkRXBytes != 300 || items[1].NetworkTXBytes != 400 || items[1].TCPRetransmits != 4 {
		t.Fatalf("normal host deltas = %+v", items[1])
	}
	if items[2].NetworkRXBytes != 0 || items[2].TCPRetransmits != 0 {
		t.Fatalf("host restart produced deltas: %+v", items[2])
	}
	if items[3].CollectionOK || items[3].MissingSources != "vnstat" || items[3].NetworkRXBytes != 20 {
		t.Fatalf("partial source handling = %+v", items[3])
	}
}

func TestStatsMappingsAreReadDynamically(t *testing.T) {
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
	if _, err := store.db.ExecContext(ctx, "INSERT INTO users(display_name, created_at) VALUES('alice', 1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO credentials(user_id, display_name, uuid, stats_user, enabled, created_at)
		SELECT id, 'phone', ?, 'alice-stats', 1, 1 FROM users WHERE display_name='alice'`, testUUID); err != nil {
		t.Fatal(err)
	}
	mappings, err := store.StatsMappings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if mappings["alice-stats"] != testUUID || len(mappings) != 1 {
		t.Fatalf("unexpected mappings: %#v", mappings)
	}
}

func TestMonthUsageReconcilesOnlySharedVnStatCoverage(t *testing.T) {
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
	start := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	if _, err := store.db.ExecContext(ctx, "INSERT INTO users(display_name, created_at) VALUES('alice', ?)", start.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO credentials(user_id, display_name, uuid, enabled, created_at)
		SELECT id, 'phone', ?, 1, ? FROM users WHERE display_name='alice'`, testUUID, start.Unix()); err != nil {
		t.Fatal(err)
	}
	insertTraffic := func(at time.Time, total int64) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, `INSERT INTO traffic_samples(
			bucket_start, credential_id, upload_delta, download_delta, active, source, collection_ok)
			SELECT ?, id, ?, 0, 1, 'test', 1 FROM credentials WHERE uuid=?`, at.Unix(), total, testUUID); err != nil {
			t.Fatal(err)
		}
	}
	insertTraffic(start.Add(time.Minute), 1000) // This precedes local vnStat coverage.
	insertTraffic(start.Add(3*time.Minute), 100)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO vnstat_snapshots(
		observed_at, interface_name, rx_total, tx_total, collection_ok) VALUES
		(?, 'eth0', 1000, 2000, 1), (?, 'eth0', 1060, 2040, 1)`,
		start.Add(2*time.Minute).Unix(), start.Add(4*time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}

	usage, err := store.MonthUsage(ctx, start, start.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if usage.UserTotalBytes != 1100 || usage.CoveredUserBytes != 100 || usage.InterfaceTotalBytes != 100 {
		t.Fatalf("unexpected coverage totals: %+v", usage)
	}
	if usage.DifferenceBytes != 0 || usage.DifferencePercent != 0 {
		t.Fatalf("different time ranges were compared: %+v", usage)
	}
}
