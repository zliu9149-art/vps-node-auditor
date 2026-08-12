package provision

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// CoreUser is the minimal sing-box inbound user representation managed by the
// provisioner.
// 中文：CoreUser 是 provisioner 管理的最小 sing-box 入站用户表示。
type CoreUser struct {
	Name string
	UUID string
	Flow string
}

// BuildCoreCandidate updates one tagged inbound and the V2Ray Stats user list
// while retaining unrelated sing-box fields byte-semantically.
// 中文：BuildCoreCandidate 更新一个具名入站和 V2Ray Stats 用户列表，同时在语义上
// 保留所有无关 sing-box 字段。
func BuildCoreCandidate(original []byte, inboundTag string, remove []CoreUser, add *CoreUser) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(original))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		return nil, errors.New("decode core configuration")
	}
	inbounds, ok := root["inbounds"].([]any)
	if !ok {
		return nil, errors.New("core configuration has no inbounds array")
	}
	matched := false
	for _, raw := range inbounds {
		inbound, ok := raw.(map[string]any)
		if !ok || inbound["tag"] != inboundTag {
			continue
		}
		if matched {
			return nil, errors.New("core configuration contains duplicate target inbound tags")
		}
		matched = true
		users, ok := inbound["users"].([]any)
		if !ok {
			return nil, errors.New("target inbound has no users array")
		}
		updated, removed, err := updateCoreUsers(users, remove, add)
		if err != nil {
			return nil, err
		}
		if removed != len(remove) {
			return nil, errors.New("active credential is absent from the target inbound")
		}
		inbound["users"] = updated
	}
	if !matched {
		return nil, errors.New("target inbound tag was not found")
	}
	if err := updateStatsUsers(root, remove, add); err != nil {
		return nil, err
	}
	result, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, errors.New("encode candidate core configuration")
	}
	return append(result, '\n'), nil
}

func updateCoreUsers(users []any, remove []CoreUser, add *CoreUser) ([]any, int, error) {
	result := make([]any, 0, len(users)+1)
	removeUUIDs := make(map[string]struct{}, len(remove))
	for _, item := range remove {
		removeUUIDs[item.UUID] = struct{}{}
	}
	removed := 0
	for _, raw := range users {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, 0, errors.New("target inbound contains an invalid user entry")
		}
		name, _ := item["name"].(string)
		uuid, _ := item["uuid"].(string)
		if _, ok := removeUUIDs[uuid]; ok {
			removed++
			continue
		}
		if add != nil && (name == add.Name || uuid == add.UUID) {
			return nil, 0, errors.New("candidate credential conflicts with an existing inbound user")
		}
		result = append(result, raw)
	}
	if add != nil {
		result = append(result, map[string]any{"name": add.Name, "uuid": add.UUID, "flow": add.Flow})
	}
	return result, removed, nil
}

func updateStatsUsers(root map[string]any, remove []CoreUser, add *CoreUser) error {
	experimental, ok := root["experimental"].(map[string]any)
	if !ok {
		return errors.New("core configuration has no experimental object")
	}
	v2ray, ok := experimental["v2ray_api"].(map[string]any)
	if !ok {
		return errors.New("core configuration has no V2Ray API object")
	}
	stats, ok := v2ray["stats"].(map[string]any)
	if !ok {
		return errors.New("core configuration has no V2Ray Stats object")
	}
	users, ok := stats["users"].([]any)
	if !ok {
		return errors.New("V2Ray Stats has no users array")
	}
	result := make([]any, 0, len(users)+1)
	removeNames := make(map[string]struct{}, len(remove))
	for _, item := range remove {
		removeNames[item.Name] = struct{}{}
	}
	removed := 0
	for _, raw := range users {
		name, ok := raw.(string)
		if !ok {
			return errors.New("V2Ray Stats contains an invalid user entry")
		}
		if _, ok := removeNames[name]; ok {
			removed++
			continue
		}
		if add != nil && name == add.Name {
			return errors.New("candidate credential conflicts with an existing Stats user")
		}
		result = append(result, name)
	}
	if removed != len(remove) {
		return fmt.Errorf("active credential is absent from V2Ray Stats users")
	}
	if add != nil {
		result = append(result, add.Name)
	}
	stats["users"] = result
	return nil
}
