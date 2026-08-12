package source

import (
	"bytes"
	"encoding/binary"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestV2RayCodecMarshalsNonResetPatternQuery(t *testing.T) {
	t.Parallel()
	codec := v2RayCodec{}
	got, err := codec.Marshal(&queryStatsRequest{Patterns: []string{"user>>>"}})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var want []byte
	want = protowire.AppendTag(want, 3, protowire.BytesType)
	want = protowire.AppendString(want, "user>>>")
	if !bytes.Equal(got, want) {
		t.Fatalf("Marshal() = %x, want %x", got, want)
	}
}

func TestDecodeUnaryGRPCFrame(t *testing.T) {
	payload := []byte{1, 2, 3}
	frame := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	got, err := decodeUnaryGRPCFrame(frame)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("decodeUnaryGRPCFrame() = %x, %v", got, err)
	}
	for _, malformed := range [][]byte{{}, {1, 0, 0, 0, 0}, {0, 0, 0, 0, 2, 1}} {
		if _, err := decodeUnaryGRPCFrame(malformed); err == nil {
			t.Fatalf("malformed frame was accepted: %x", malformed)
		}
	}
}

func TestV2RayCodecUnmarshalsStatsResponse(t *testing.T) {
	t.Parallel()
	data := appendStatMessage(nil, "user>>>alice>>>traffic>>>uplink", 123)
	data = appendStatMessage(data, "user>>>alice>>>traffic>>>downlink", 456)
	response := &queryStatsResponse{}
	if err := (v2RayCodec{}).Unmarshal(data, response); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(response.Stats) != 2 {
		t.Fatalf("stats length = %d", len(response.Stats))
	}
	if response.Stats[0].Value != 123 || response.Stats[1].Value != 456 {
		t.Fatalf("stats = %#v", response.Stats)
	}
}

func TestV2RayCodecRejectsMalformedResponse(t *testing.T) {
	t.Parallel()
	response := &queryStatsResponse{}
	if err := (v2RayCodec{}).Unmarshal([]byte{0x0a, 0xff}, response); err == nil {
		t.Fatal("Unmarshal() accepted malformed protobuf")
	}
}

func appendStatMessage(data []byte, name string, value int64) []byte {
	var message []byte
	message = protowire.AppendTag(message, 1, protowire.BytesType)
	message = protowire.AppendString(message, name)
	message = protowire.AppendTag(message, 2, protowire.VarintType)
	message = protowire.AppendVarint(message, uint64(value))
	data = protowire.AppendTag(data, 1, protowire.BytesType)
	return protowire.AppendBytes(data, message)
}
