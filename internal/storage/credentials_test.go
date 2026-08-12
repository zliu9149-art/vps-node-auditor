package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"vps-node-auditor/internal/config"
)

func TestCredentialAddRotateDisableAndAmbiguity(t *testing.T) {
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
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	secondUUID := "22222222-2222-4222-8222-222222222222"
	if err := store.AddCredential(ctx, "alice", "phone", testUUID, "alice-phone-stats", now); err != nil {
		t.Fatal(err)
	}
	if err := store.AddCredential(ctx, "alice", "laptop", secondUUID, "alice-laptop-stats", now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActiveCredential(ctx, "alice", ""); err == nil {
		t.Fatal("multiple active credentials did not require an explicit name")
	}
	phone, err := store.ActiveCredential(ctx, "alice", "phone")
	if err != nil {
		t.Fatal(err)
	}
	replacementUUID := "33333333-3333-4333-8333-333333333333"
	if err := store.RotateCredential(ctx, phone.ID, "phone-2", replacementUUID, "alice-phone-2-stats", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	laptop, err := store.ActiveCredential(ctx, "alice", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DisableCredential(ctx, laptop.ID, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.SyncCredentials(ctx, []config.CredentialConfig{{
		UserName: "alice", CredentialName: "laptop", UUID: secondUUID,
		StatsUser: "alice-laptop-stats", Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActiveCredential(ctx, "alice", "laptop"); err == nil {
		t.Fatal("collector credential sync re-enabled a revoked credential")
	}
	items, err := store.ListCredentials(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].Enabled || items[1].Enabled || !items[2].Enabled {
		t.Fatalf("credential history = %+v", items)
	}
	mappings, err := store.StatsMappings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(mappings) != 1 || mappings["alice-phone-2-stats"] != replacementUUID {
		t.Fatalf("active mappings = %#v", mappings)
	}
}

func TestCredentialNameCanBeReusedAfterRevocation(t *testing.T) {
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
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	if err := store.AddCredential(ctx, "alice", "phone", testUUID, "stats-old", now); err != nil {
		t.Fatal(err)
	}
	if err := store.AddCredential(ctx, "alice", "phone",
		"22222222-2222-4222-8222-222222222222", "stats-duplicate", now); err == nil {
		t.Fatal("duplicate active credential name was accepted")
	}
	active, err := store.ActiveCredential(ctx, "alice", "phone")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DisableCredential(ctx, active.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.AddCredential(ctx, "alice", "phone",
		"33333333-3333-4333-8333-333333333333", "stats-new", now.Add(2*time.Hour)); err != nil {
		t.Fatalf("reuse revoked credential name: %v", err)
	}
	items, err := store.ListCredentials(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Enabled || !items[1].Enabled ||
		items[0].CredentialName != "phone" || items[1].CredentialName != "phone" {
		t.Fatalf("credential history = %+v", items)
	}
}
