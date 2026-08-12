package provision

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vps-node-auditor/internal/domain"
)

func TestCollectorFreshnessUsesTrafficSamplesNotDatabaseMtime(t *testing.T) {
	now := time.Now().UTC()
	for _, test := range []struct {
		name   string
		health domain.Health
		want   CheckStatus
	}{
		{"recent traffic", domain.Health{LastTrafficSampleAt: &now, TrafficFresh: true}, CheckOK},
		{"stale traffic", domain.Health{LastTrafficSampleAt: &now, TrafficFresh: false}, CheckError},
		{"no traffic", domain.Health{}, CheckError},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got CheckStatus
			checkCollectorFreshness(test.health, nil, func(_ string, status CheckStatus, _ string) { got = status })
			if got != test.want {
				t.Fatalf("freshness status = %q, want %q", got, test.want)
			}
		})
	}
}

func TestStatsMappingComparisonAllowsActiveUsersWithoutCounters(t *testing.T) {
	for _, test := range []struct {
		name                   string
		runtime, database      []string
		unexpected, unobserved int
	}{
		{"exact", []string{"a", "b"}, []string{"a", "b"}, 0, 0},
		{"zero traffic user", []string{"a"}, []string{"a", "b"}, 0, 1},
		{"unexpected runtime user", []string{"a", "x"}, []string{"a", "b"}, 1, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			unexpected, unobserved := setDifferenceCounts(test.runtime, test.database)
			if unexpected != test.unexpected || unobserved != test.unobserved {
				t.Fatalf("differences = %d, %d; want %d, %d",
					unexpected, unobserved, test.unexpected, test.unobserved)
			}
		})
	}
}

func TestReadCoreCredentialSetsRejectsMalformedStructure(t *testing.T) {
	for _, contents := range []string{`{}`, `{"inbounds":[],"experimental":{}}`,
		`{"inbounds":[{"tag":"proxy-in"}],"experimental":{}}`} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readCoreCredentialSets(path, "proxy-in"); err == nil {
			t.Fatalf("malformed configuration was accepted: %s", contents)
		}
	}
}

func TestCheckYAMLFilesClassifiesMissingMismatchAndRevokedResidual(t *testing.T) {
	cfg := testConfig(t.TempDir())
	if err := os.MkdirAll(cfg.OutputDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	active := []domain.Credential{
		{UserName: "alice", DisplayName: "missing", UUID: "11111111-1111-4111-8111-111111111111"},
		{UserName: "alice", DisplayName: "wrong", UUID: "22222222-2222-4222-8222-222222222222"},
	}
	if err := os.WriteFile(filepath.Join(cfg.OutputDirectory, "alice-wrong.yaml"), []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.OutputDirectory, "bob-old.yaml"), []byte("revoked"), 0o600); err != nil {
		t.Fatal(err)
	}
	summaries := []domain.CredentialSummary{{UserName: "bob", CredentialName: "old", Enabled: false}}
	var items []CheckItem
	checkYAMLFiles(cfg, active, summaries, func(name string, status CheckStatus, message string) {
		items = append(items, CheckItem{Name: name, Status: status, Message: message})
	})
	if len(items) != 3 || items[0].Status != CheckWarning || !strings.Contains(items[0].Message, "1 active") {
		t.Fatalf("active YAML presence = %#v", items)
	}
	if items[1].Status != CheckError || !strings.Contains(items[1].Message, "1 active") {
		t.Fatalf("active YAML content = %#v", items)
	}
	if items[2].Status != CheckWarning || !strings.Contains(items[2].Message, "1 revoked") {
		t.Fatalf("revoked YAML result = %#v", items)
	}
}

func TestCheckYAMLFilesTreatsMissingActiveYAMLAsWarning(t *testing.T) {
	cfg := testConfig(t.TempDir())
	var items []CheckItem
	checkYAMLFiles(cfg, []domain.Credential{{UserName: "alice", DisplayName: "phone", UUID: "uuid"}}, nil,
		func(name string, status CheckStatus, message string) {
			items = append(items, CheckItem{Name: name, Status: status, Message: message})
		})
	if len(items) != 3 || items[0].Status != CheckWarning || items[1].Status != CheckOK || items[2].Status != CheckOK {
		t.Fatalf("YAML results = %#v", items)
	}
}

func TestCheckYAMLFilesDoesNotTreatReusedActiveNameAsRevokedResidual(t *testing.T) {
	cfg := testConfig(t.TempDir())
	if err := os.MkdirAll(cfg.OutputDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	uuid := "33333333-3333-4333-8333-333333333333"
	data := strings.Join([]string{uuid, cfg.Server, cfg.SNI}, "\n")
	if err := os.WriteFile(filepath.Join(cfg.OutputDirectory, "alice-phone.yaml"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	active := []domain.Credential{{UserName: "alice", DisplayName: "phone", UUID: uuid}}
	summaries := []domain.CredentialSummary{
		{UserName: "alice", CredentialName: "phone", Enabled: false},
		{UserName: "alice", CredentialName: "phone", Enabled: true},
	}
	var items []CheckItem
	checkYAMLFiles(cfg, active, summaries, func(name string, status CheckStatus, message string) {
		items = append(items, CheckItem{Name: name, Status: status, Message: message})
	})
	if len(items) != 3 || items[0].Status != CheckOK || items[1].Status != CheckOK ||
		items[2].Status != CheckOK {
		t.Fatalf("YAML results = %#v", items)
	}
}
