package source

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQueryV2RayStatsOverLoopbackHTTP2(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.ProtoMajor != 2 || request.URL.Path != queryStatsMethod ||
			request.Header.Get("Content-Type") != "application/grpc" {
			http.Error(response, "bad request", http.StatusBadRequest)
			return
		}
		response.Header().Set("Content-Type", "application/grpc")
		response.Header().Set("Trailer", "Grpc-Status")
		payload := appendStatMessage(nil, "user>>>alice>>>traffic>>>uplink", 123)
		frame := make([]byte, 5+len(payload))
		binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
		copy(frame[5:], payload)
		_, _ = response.Write(frame)
		response.Header().Set("Grpc-Status", "0")
	})}
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	server.Protocols = protocols
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := queryV2RayStats(ctx, listener.Addr().String(),
		&queryStatsRequest{Patterns: []string{"user>>>"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Stats) != 1 || result.Stats[0].Value != 123 {
		t.Fatalf("response = %#v", result.Stats)
	}
	source := NewV2RaySource(listener.Addr().String(), 3*time.Second,
		func(context.Context) (map[string]string, error) {
			return map[string]string{"alice": "internal-credential-key"}, nil
		})
	source.mainPID = func(context.Context) (string, error) { return "", errors.New("temporarily unavailable") }
	source.now = func() time.Time { return time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC) }
	snapshot, err := source.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CoreBootID != "" || snapshot.Users["internal-credential-key"].Upload != 123 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestMapV2RayCountersMapsConfiguredUsersOnly(t *testing.T) {
	t.Parallel()
	mapping := map[string]string{
		"alice-stats": "11111111-1111-4111-8111-111111111111",
		"bob-stats":   "22222222-2222-4222-8222-222222222222",
	}
	stats := []v2RayStat{
		{Name: "user>>>alice-stats>>>traffic>>>uplink", Value: 100},
		{Name: "user>>>alice-stats>>>traffic>>>downlink", Value: 900},
		{Name: "user>>>unknown>>>traffic>>>uplink", Value: 500},
		{Name: "inbound>>>proxy-in>>>traffic>>>uplink", Value: 600},
	}
	users, err := mapV2RayCounters(stats, mapping)
	if err != nil {
		t.Fatalf("mapV2RayCounters() error = %v", err)
	}
	alice := users[mapping["alice-stats"]]
	if alice.Upload != 100 || alice.Download != 900 {
		t.Fatalf("alice counters = %#v", alice)
	}
	bob := users[mapping["bob-stats"]]
	if bob.Upload != 0 || bob.Download != 0 {
		t.Fatalf("bob counters = %#v", bob)
	}
}

func TestMapV2RayCountersRejectsDuplicateDirectionWithoutLeakingUser(t *testing.T) {
	t.Parallel()
	privateName := "private-user-label"
	stats := []v2RayStat{
		{Name: "user>>>" + privateName + ">>>traffic>>>uplink", Value: 1},
		{Name: "user>>>" + privateName + ">>>traffic>>>uplink", Value: 2},
	}
	_, err := mapV2RayCounters(stats, map[string]string{privateName: "uuid"})
	if err == nil {
		t.Fatal("mapV2RayCounters() accepted a duplicate counter")
	}
	if strings.Contains(err.Error(), privateName) {
		t.Fatalf("error leaked statistical user name: %v", err)
	}
}

func TestReadProcessIdentityUsesBootIDPIDAndStartTime(t *testing.T) {
	t.Parallel()
	procRoot := t.TempDir()
	mustWriteTestFile(t, filepath.Join(procRoot, "sys", "kernel", "random", "boot_id"), "boot-id\n")
	mustWriteTestFile(t, filepath.Join(procRoot, "123", "comm"), "sing-box\n")
	fields := []string{"S"}
	for len(fields) < 20 {
		fields = append(fields, "0")
	}
	fields[19] = "777"
	mustWriteTestFile(t, filepath.Join(procRoot, "123", "stat"), "123 (sing-box) "+strings.Join(fields, " ")+"\n")

	identity, err := readProcessIdentity(procRoot, "123")
	if err != nil {
		t.Fatalf("readProcessIdentity() error = %v", err)
	}
	if identity != "boot-id:123:777" {
		t.Fatalf("identity = %q", identity)
	}
}

func mustWriteTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
