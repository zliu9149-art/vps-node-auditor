package collector

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"vps-node-auditor/internal/domain"
	"vps-node-auditor/internal/lockfile"
)

func TestCollectOnceRecordsSourceFailureAsGap(t *testing.T) {
	t.Parallel()
	sourceErr := errors.New("unavailable")
	repository := &fakeRepository{}
	worker := New(fakeSource{err: sourceErr}, repository)
	worker.now = func() time.Time { return time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC) }

	err := worker.CollectOnce(context.Background())
	if !errors.Is(err, sourceErr) {
		t.Fatalf("CollectOnce() error = %v", err)
	}
	if repository.failures != 1 {
		t.Fatalf("failure records = %d", repository.failures)
	}
	if repository.snapshots != 0 {
		t.Fatalf("snapshot records = %d", repository.snapshots)
	}
}

func TestCollectOnceRecordsSnapshot(t *testing.T) {
	t.Parallel()
	repository := &fakeRepository{}
	worker := New(fakeSource{snapshot: domain.StatsSnapshot{
		ObservedAt: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
		Users:      map[string]domain.Counters{"uuid": {Upload: 1, Download: 2}},
	}}, repository)
	if err := worker.CollectOnce(context.Background()); err != nil {
		t.Fatalf("CollectOnce() error = %v", err)
	}
	if repository.snapshots != 1 || repository.failures != 0 {
		t.Fatalf("repository calls = snapshots:%d failures:%d", repository.snapshots, repository.failures)
	}
}

func TestCollectOnceSkipsWhileProvisionLockIsHeld(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.lock")
	held, busy, err := lockfile.TryAcquire(path)
	if err != nil || busy {
		t.Fatalf("hold lock: busy=%v err=%v", busy, err)
	}
	defer held.Close()
	repository := &fakeRepository{}
	worker := New(fakeSource{snapshot: domain.StatsSnapshot{ObservedAt: time.Now()}}, repository).WithCycleLock(path)
	if err := worker.CollectOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.snapshots != 0 || repository.failures != 0 {
		t.Fatalf("locked cycle touched repository: %+v", repository)
	}
}

type fakeSource struct {
	snapshot domain.StatsSnapshot
	err      error
}

func (fakeSource) Name() string { return "fake" }

func (s fakeSource) Collect(context.Context) (domain.StatsSnapshot, error) {
	return s.snapshot, s.err
}

type fakeRepository struct {
	snapshots int
	failures  int
}

func (r *fakeRepository) RecordSnapshot(context.Context, string, domain.StatsSnapshot) error {
	r.snapshots++
	return nil
}

func (r *fakeRepository) RecordFailure(context.Context, string, time.Time) error {
	r.failures++
	return nil
}
