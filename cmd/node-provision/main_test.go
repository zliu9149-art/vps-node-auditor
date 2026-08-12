package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vps-node-auditor/internal/storage"
)

func TestListOutputNeverContainsUUID(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	database := filepath.Join(directory, "audit.db")
	store, err := storage.Open(database, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	uuid := "11111111-1111-4111-8111-111111111111"
	if err := store.AddCredential(context.Background(), "alice", "phone", uuid, "alice-stats", time.Now()); err != nil {
		t.Fatal(err)
	}
	store.Close()
	configPath := filepath.Join(directory, "config.json")
	configJSON := fmt.Sprintf(`{
		"database":{"path":%q},
		"collector":{"source":"mock","mock_file":"mock.json"},
		"display":{"timezone":"UTC"},
		"provider":{"cycle_start_day":9,"traffic_direction":"both"}
	}`, filepath.ToSlash(database))
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(context.Background(), []string{"--config", configPath, "list", "--json"}, &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), uuid) || !strings.Contains(output.String(), "phone") {
		t.Fatalf("unsafe list output: %s", output.String())
	}
}

func TestParseRequestExecutesDirectlyAndRejectsApply(t *testing.T) {
	request, err := parseRequest("add", []string{"--user", "alice", "--credential", "phone"})
	if err != nil {
		t.Fatal(err)
	}
	if request.Operation != "add" {
		t.Fatalf("operation = %q", request.Operation)
	}
	if _, err := parseRequest("add", []string{"--user", "alice", "--credential", "phone", "--apply"}); err == nil {
		t.Fatal("removed --apply option was accepted")
	}
}

func TestParseRemoveUser(t *testing.T) {
	request, err := parseRequest("remove-user", []string{"--user", "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if request.Operation != "remove-user" || request.UserName != "alice" {
		t.Fatalf("request = %+v", request)
	}
	if _, err := parseRequest("remove-user", []string{"--user", "alice", "--credential", "phone"}); err == nil {
		t.Fatal("remove-user accepted a device selector")
	}
}
