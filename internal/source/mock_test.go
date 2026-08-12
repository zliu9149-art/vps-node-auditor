package source

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMockFileSourceCollect(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "stats.json")
	data := `{
  "observed_at":"2026-08-09T12:00:00Z",
  "core_boot_id":"boot-a",
  "users":[{"uuid":"secret-value","upload_total":100,"download_total":200}]
}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewMockFileSource(path).Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if snapshot.CoreBootID != "boot-a" {
		t.Fatalf("core boot ID = %q", snapshot.CoreBootID)
	}
	if !snapshot.ObservedAt.Equal(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("observed time = %v", snapshot.ObservedAt)
	}
	if got := snapshot.Users["secret-value"]; got.Upload != 100 || got.Download != 200 {
		t.Fatalf("counters = %+v", got)
	}
}

func TestMockFileSourceRejectsNegativeCountersWithoutEchoingUUID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "stats.json")
	secret := "do-not-echo-this-uuid"
	data := `{"users":[{"uuid":"` + secret + `","upload_total":-1,"download_total":0}]}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewMockFileSource(path).Collect(context.Background())
	if err == nil {
		t.Fatal("Collect() accepted a negative counter")
	}
	if contains(err.Error(), secret) {
		t.Fatalf("error exposed UUID: %v", err)
	}
}

func contains(value, fragment string) bool {
	for i := 0; i+len(fragment) <= len(value); i++ {
		if value[i:i+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
