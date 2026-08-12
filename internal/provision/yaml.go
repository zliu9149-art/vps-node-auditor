package provision

import (
	"encoding/json"
	"fmt"
	"strings"
)

// BuildMihomoYAML returns a complete standalone Mihomo/Clash Meta document.
// It contains the client UUID but never a Reality private key.
// 中文：BuildMihomoYAML 返回完整、独立的 Mihomo/Clash Meta 文档；其中包含客户端
// UUID，但绝不包含 Reality 私钥。
func BuildMihomoYAML(cfg Config, displayName, uuid string) ([]byte, error) {
	if displayName == "" || uuid == "" {
		return nil, fmt.Errorf("display name and UUID are required")
	}
	quote := func(value string) string {
		data, _ := json.Marshal(value)
		return string(data)
	}
	groupName := "代理选择"
	var output strings.Builder
	fmt.Fprintf(&output, "mixed-port: %d\n", cfg.MixedPort)
	output.WriteString("allow-lan: false\nmode: rule\nlog-level: info\nipv6: true\n")
	output.WriteString("proxies:\n")
	fmt.Fprintf(&output, "  - name: %s\n", quote(displayName))
	output.WriteString("    type: vless\n")
	fmt.Fprintf(&output, "    server: %s\n    port: %d\n", quote(cfg.Server), cfg.Port)
	fmt.Fprintf(&output, "    uuid: %s\n", quote(uuid))
	output.WriteString("    network: tcp\n    udp: true\n    tls: true\n")
	fmt.Fprintf(&output, "    flow: %s\n    servername: %s\n", quote(cfg.Flow), quote(cfg.SNI))
	output.WriteString("    reality-opts:\n")
	fmt.Fprintf(&output, "      public-key: %s\n      short-id: %s\n", quote(cfg.RealityPublicKey), quote(cfg.ShortID))
	output.WriteString("    client-fingerprint: chrome\n")
	output.WriteString("proxy-groups:\n")
	fmt.Fprintf(&output, "  - name: %s\n    type: select\n    proxies:\n", quote(groupName))
	fmt.Fprintf(&output, "      - %s\n      - DIRECT\n", quote(displayName))
	output.WriteString("rules:\n")
	fmt.Fprintf(&output, "  - %s\n", quote("MATCH,"+groupName))
	return []byte(output.String()), nil
}
