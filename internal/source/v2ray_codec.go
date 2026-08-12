package source

import (
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"
)

// v2RayCodec implements only the protobuf messages required by QueryStats.
// Keeping the codec narrow avoids importing a proxy core into the auditor.
// 中文：v2RayCodec 只实现 QueryStats 所需 protobuf 消息，避免为审计器引入完整代理核心。
type v2RayCodec struct{}

// queryStatsRequest is the minimal wire representation of V2Ray QueryStats.
// 中文：queryStatsRequest 是 V2Ray QueryStats 请求的最小线协议表示。
type queryStatsRequest struct {
	Patterns []string
	Reset    bool
}

// queryStatsResponse is the minimal wire representation of a QueryStats reply.
// 中文：queryStatsResponse 是 QueryStats 响应的最小线协议表示。
type queryStatsResponse struct {
	Stats []v2RayStat
}

// v2RayStat contains one named cumulative counter returned by the core.
// 中文：v2RayStat 保存核心返回的一个具名累计计数器。
type v2RayStat struct {
	Name  string
	Value int64
}

// Marshal encodes only the supported QueryStats request fields.
// 中文：Marshal 只编码受支持的 QueryStats 请求字段。
func (v2RayCodec) Marshal(value any) ([]byte, error) {
	request, ok := value.(*queryStatsRequest)
	if !ok {
		return nil, fmt.Errorf("unsupported V2Ray request type %T", value)
	}
	var data []byte
	if request.Reset {
		data = protowire.AppendTag(data, 2, protowire.VarintType)
		data = protowire.AppendVarint(data, 1)
	}
	for _, pattern := range request.Patterns {
		data = protowire.AppendTag(data, 3, protowire.BytesType)
		data = protowire.AppendString(data, pattern)
	}
	return data, nil
}

// Unmarshal decodes QueryStats replies and safely skips unknown protobuf fields
// for forward compatibility with newer core versions.
// 中文：Unmarshal 解码 QueryStats 响应并安全跳过未知字段，以兼容更新核心版本。
func (v2RayCodec) Unmarshal(data []byte, value any) error {
	response, ok := value.(*queryStatsResponse)
	if !ok {
		return fmt.Errorf("unsupported V2Ray response type %T", value)
	}
	response.Stats = nil
	for len(data) > 0 {
		number, fieldType, tagLength := protowire.ConsumeTag(data)
		if tagLength < 0 {
			return fmt.Errorf("decode V2Ray response tag: %v", protowire.ParseError(tagLength))
		}
		data = data[tagLength:]
		if number == 1 && fieldType == protowire.BytesType {
			message, fieldLength := protowire.ConsumeBytes(data)
			if fieldLength < 0 {
				return fmt.Errorf("decode V2Ray stat message: %v", protowire.ParseError(fieldLength))
			}
			stat, err := decodeV2RayStat(message)
			if err != nil {
				return err
			}
			response.Stats = append(response.Stats, stat)
			data = data[fieldLength:]
			continue
		}
		fieldLength := protowire.ConsumeFieldValue(number, fieldType, data)
		if fieldLength < 0 {
			return fmt.Errorf("skip V2Ray response field: %v", protowire.ParseError(fieldLength))
		}
		data = data[fieldLength:]
	}
	return nil
}

// decodeV2RayStat decodes one nested stat message without accepting malformed
// tags or lengths.
// 中文：decodeV2RayStat 解码一个嵌套统计消息，并拒绝畸形标签或长度。
func decodeV2RayStat(data []byte) (v2RayStat, error) {
	var stat v2RayStat
	for len(data) > 0 {
		number, fieldType, tagLength := protowire.ConsumeTag(data)
		if tagLength < 0 {
			return v2RayStat{}, fmt.Errorf("decode V2Ray stat tag: %v", protowire.ParseError(tagLength))
		}
		data = data[tagLength:]
		switch {
		case number == 1 && fieldType == protowire.BytesType:
			name, fieldLength := protowire.ConsumeString(data)
			if fieldLength < 0 {
				return v2RayStat{}, fmt.Errorf("decode V2Ray stat name: %v", protowire.ParseError(fieldLength))
			}
			stat.Name = name
			data = data[fieldLength:]
		case number == 2 && fieldType == protowire.VarintType:
			value, fieldLength := protowire.ConsumeVarint(data)
			if fieldLength < 0 {
				return v2RayStat{}, fmt.Errorf("decode V2Ray stat value: %v", protowire.ParseError(fieldLength))
			}
			stat.Value = int64(value)
			data = data[fieldLength:]
		default:
			fieldLength := protowire.ConsumeFieldValue(number, fieldType, data)
			if fieldLength < 0 {
				return v2RayStat{}, fmt.Errorf("skip V2Ray stat field: %v", protowire.ParseError(fieldLength))
			}
			data = data[fieldLength:]
		}
	}
	return stat, nil
}
