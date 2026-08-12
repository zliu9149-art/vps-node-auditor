package provision

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const coreFixture = `{
  "log":{"level":"info"},
  "inbounds":[{"type":"vless","tag":"proxy-in","listen_port":443,"users":[
    {"name":"old-stats","uuid":"11111111-1111-4111-8111-111111111111","flow":"xtls-rprx-vision"}
  ]}],
  "experimental":{"v2ray_api":{"listen":"127.0.0.1:10085","stats":{"enabled":true,"inbounds":["proxy-in"],"users":["old-stats"]}}}
}`

func TestBuildCoreCandidateRotatesUserAndStatsMapping(t *testing.T) {
	old := &CoreUser{Name: "old-stats", UUID: "11111111-1111-4111-8111-111111111111", Flow: "xtls-rprx-vision"}
	replacement := &CoreUser{Name: "new-stats", UUID: "22222222-2222-4222-8222-222222222222", Flow: "xtls-rprx-vision"}
	data, err := BuildCoreCandidate([]byte(coreFixture), "proxy-in", []CoreUser{*old}, replacement)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), old.UUID) || strings.Contains(string(data), old.Name) {
		t.Fatal("old credential remains in candidate")
	}
	if !strings.Contains(string(data), replacement.UUID) || !strings.Contains(string(data), replacement.Name) {
		t.Fatal("replacement credential is missing")
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("candidate is not valid JSON: %v", err)
	}
}

func TestBuildMihomoYAMLHasRequiredRealityFields(t *testing.T) {
	cfg := testConfig(t.TempDir())
	data, err := BuildMihomoYAML(cfg, "alice-phone", "11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("generated document is not parseable YAML: %v", err)
	}
	if _, ok := parsed["proxies"].([]any); !ok {
		t.Fatalf("parsed YAML has no proxies sequence: %#v", parsed)
	}
	for _, required := range []string{
		"mixed-port:", "proxies:", "type: vless", "flow: \"xtls-rprx-vision\"",
		"tls: true", "servername:", "reality-opts:", "public-key:", "short-id:",
		"proxy-groups:", "rules:", "MATCH,代理选择",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("YAML missing %q\n%s", required, text)
		}
	}
	if strings.Contains(strings.ToLower(text), "private") {
		t.Fatal("YAML contains a server private-key field")
	}
}
