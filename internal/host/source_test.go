package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type fakeRunner map[string][]byte

func (r fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := name
	if len(args) > 0 {
		key += " " + args[0]
	}
	data, ok := r[key]
	if !ok {
		return nil, errors.New("fixture command unavailable")
	}
	return data, nil
}

func TestParsersReadLinuxFixtures(t *testing.T) {
	total, idle, ok := parseCPUStat([]byte("cpu  100 2 30 400 5 6 7 8\n"))
	if !ok || total != 558 || idle != 405 {
		t.Fatalf("parseCPUStat() = %d, %d, %v", total, idle, ok)
	}
	memory, swap, ok := parseMemInfo([]byte("MemTotal: 1000 kB\nMemFree: 100 kB\nBuffers: 50 kB\nCached: 200 kB\nSReclaimable: 20 kB\nShmem: 10 kB\nSwapTotal: 500 kB\nSwapFree: 400 kB\n"))
	if !ok || memory != 640*1024 || swap != 100*1024 {
		t.Fatalf("parseMemInfo() = %d, %d, %v", memory, swap, ok)
	}
	rx, tx, drops, ok := parseNetDev([]byte("eth0: 100 1 2 3 4 5 6 7 200 8 9 10 11 12 13 14\n"), "eth0")
	if !ok || rx != 100 || tx != 200 || drops != 13 {
		t.Fatalf("parseNetDev() = %d, %d, %d, %v", rx, tx, drops, ok)
	}
	retrans, resets, ok := parseNStat([]byte("TcpRetransSegs 7 0.0\nTcpEstabResets 2 0.0\nTcpOutRsts 3 0.0\n"))
	if !ok || retrans != 7 || resets != 5 {
		t.Fatalf("parseNStat() = %d, %d, %v", retrans, resets, ok)
	}
}

func TestParseVnStat(t *testing.T) {
	payload := []byte(`{"interfaces":[{"name":"eth0","created":{"date":{"year":2026,"month":8,"day":9},"time":{"hour":1,"minute":2}},"traffic":{"total":{"rx":123,"tx":456}}}]}`)
	item, err := parseVnStat(payload, "eth0")
	if err != nil {
		t.Fatal(err)
	}
	if item.RXTotal != 123 || item.TXTotal != 456 || !item.CollectionOK || item.CoverageStart.IsZero() {
		t.Fatalf("unexpected vnStat snapshot: %+v", item)
	}
}

func TestReadSysNetFallback(t *testing.T) {
	root := t.TempDir()
	statistics := filepath.Join(root, "class", "net", "eth0", "statistics")
	if err := os.MkdirAll(statistics, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"rx_bytes": "100\n", "tx_bytes": "200\n", "rx_dropped": "3\n", "tx_dropped": "4\n",
	} {
		if err := os.WriteFile(filepath.Join(statistics, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rx, tx, drops, ok := readSysNet(root, "eth0")
	if !ok || rx != 100 || tx != 200 || drops != 7 {
		t.Fatalf("readSysNet() = %d, %d, %d, %v", rx, tx, drops, ok)
	}
}

func TestCollectKeepsPartialEvidence(t *testing.T) {
	root := t.TempDir()
	mustWrite := func(name, value string) {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("sys/kernel/random/boot_id", "test-boot\n")
	mustWrite("stat", "cpu 10 0 5 100 0 0 0 0\n")
	mustWrite("loadavg", "0.25 0.2 0.1 1/2 3\n")
	mustWrite("meminfo", "MemTotal: 1000 kB\nMemFree: 100 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n")
	mustWrite("net/dev", "eth0: 100 0 0 0 0 0 0 0 200 0 0 0 0 0 0 0\n")
	source := &LinuxSource{
		InterfaceName: "eth0",
		ProxyProcess:  "sing-box",
		ProcRoot:      root,
		Runner: fakeRunner{
			"nstat -az": []byte("TcpRetransSegs 7\nTcpEstabResets 2\n"),
			"ss -Htan":  []byte("ESTAB row\n"),
		},
		Now: func() time.Time { return time.Unix(100, 0) },
	}
	item := source.Collect(context.Background())
	if !item.CPUValid || !item.MemoryValid || !item.NetworkValid || !item.TCPValid || !item.ConnectionsValid {
		t.Fatalf("available evidence was rejected: %+v", item)
	}
	wantMissing := []string{"vnstat"}
	if !reflect.DeepEqual(item.MissingSources, wantMissing) {
		t.Fatalf("MissingSources = %#v, want %#v", item.MissingSources, wantMissing)
	}
}
