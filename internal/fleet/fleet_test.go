package fleet_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/arti1117/fleet-master-controller/internal/fleet"
	"github.com/arti1117/fleet-master-controller/internal/ledger"
)

// testClock yields a deterministic, strictly increasing time. It is only ever
// called from the Core's single owner goroutine, so the shared counter is safe.
func testClock() fleet.Clock {
	var n int64
	return func() time.Time {
		n++
		return time.Unix(n, 0).UTC()
	}
}

// The headline property: assignments accepted before a crash are recovered
// exactly — no loss, no duplication — from the durable WAL after restart.
func TestAssignAndRecover_NoLossNoDuplication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.wal")

	// --- run 1: assign, then "crash" (close without any completion step) ---
	led, err := ledger.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	core, err := fleet.Open(led, fleet.WithClock(testClock()))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"pick-A": "agv-01", "pick-B": "agv-02", "drop-A": "agv-01"}
	for task, robot := range want {
		if err := core.AssignTask(task, robot); err != nil {
			t.Fatalf("AssignTask(%s,%s): %v", task, robot, err)
		}
	}
	before := core.Snapshot()
	core.Close()
	if err := led.Close(); err != nil { // process exit
		t.Fatal(err)
	}

	// --- run 2: fresh process recovers purely from the WAL ---
	led2, err := ledger.Open(path)
	if err != nil {
		t.Fatalf("reopen WAL: %v", err)
	}
	defer led2.Close()
	core2, err := fleet.Open(led2, fleet.WithClock(testClock()))
	if err != nil {
		t.Fatal(err)
	}
	defer core2.Close()

	after := core2.Snapshot()
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("recovered state differs:\n before=%v\n after =%v", before, after)
	}
	if !reflect.DeepEqual(after, want) {
		t.Fatalf("recovered state wrong:\n got =%v\n want=%v", after, want)
	}
	if broken, err := led2.Verify(); broken != -1 || err != nil {
		t.Fatalf("audit chain not intact after recovery: broken@%d %v", broken, err)
	}
}

// Demo ①: a task already assigned cannot be reassigned by a plain AssignTask.
func TestAssignTask_CollisionFree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.wal")
	led, _ := ledger.Open(path)
	defer led.Close()
	core, _ := fleet.Open(led, fleet.WithClock(testClock()))
	defer core.Close()

	if err := core.AssignTask("t0", "r1"); err != nil {
		t.Fatalf("first assign: %v", err)
	}
	err := core.AssignTask("t0", "r2")
	if err == nil {
		t.Fatal("expected double-assign to be rejected")
	}
	if !errors.Is(err, fleet.ErrAlreadyAssigned) {
		t.Fatalf("want ErrAlreadyAssigned, got %v", err)
	}
	if got := core.Snapshot()["t0"]; got != "r1" {
		t.Fatalf("t0 = %q, want r1 (first writer wins)", got)
	}
}

// Close must be idempotent and safe under concurrency (no double close-of-channel panic).
func TestClose_Idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.wal")
	led, _ := ledger.Open(path)
	defer led.Close()
	core, _ := fleet.Open(led, fleet.WithClock(testClock()))

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); core.Close() }()
	}
	wg.Wait()

	// Operations after close return ErrClosed, not panic.
	if err := core.AssignTask("t", "r"); !errors.Is(err, fleet.ErrClosed) {
		t.Fatalf("want ErrClosed after close, got %v", err)
	}
}

// Demo ①: many goroutines racing to claim the SAME task -> exactly one winner,
// and no data race on the assignment map (run with -race).
func TestConcurrentAssign_SameTaskExactlyOneWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.wal")
	led, _ := ledger.Open(path)
	defer led.Close()
	core, _ := fleet.Open(led, fleet.WithClock(testClock()))
	defer core.Close()

	const racers = 32
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := core.AssignTask("hot", fmt.Sprintf("agv-%02d", i)); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	if wins != 1 {
		t.Fatalf("want exactly 1 winner for the contended task, got %d", wins)
	}
	if led.Len() != 1 {
		t.Fatalf("want exactly 1 ledger entry for the contended task, got %d", led.Len())
	}
}

// Distinct tasks assigned concurrently all succeed and all persist.
func TestConcurrentAssign_DistinctTasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.wal")
	led, _ := ledger.Open(path)
	defer led.Close()
	core, _ := fleet.Open(led, fleet.WithClock(testClock()))
	defer core.Close()

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = core.AssignTask(fmt.Sprintf("task-%03d", i), "agv-01")
		}(i)
	}
	wg.Wait()

	if got := len(core.Snapshot()); got != n {
		t.Fatalf("want %d assignments, got %d", n, got)
	}
	if broken, err := led.Verify(); broken != -1 || err != nil {
		t.Fatalf("chain broken under concurrency at %d: %v", broken, err)
	}
}

// P1 acceptance (load): N racers contend for each of M tasks — 1,600 goroutines
// at once. Every task must get exactly one winner (winner total == task count),
// losers must be rejected with ErrAlreadyAssigned before touching the WAL
// (exactly one durable entry per task), and the whole run must be -race clean.
func TestStress_ManyContendedTasks_ExactlyOneWinnerEach(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.wal")
	led, err := ledger.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer led.Close()
	core, err := fleet.Open(led, fleet.WithClock(testClock()))
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()

	const (
		tasks         = 100
		racersPerTask = 16
	)
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins = make(map[string]int, tasks) // taskID -> number of successful claims
	)
	for ti := 0; ti < tasks; ti++ {
		taskID := fmt.Sprintf("task-%03d", ti)
		for r := 0; r < racersPerTask; r++ {
			wg.Add(1)
			go func(taskID, robotID string) {
				defer wg.Done()
				switch err := core.AssignTask(taskID, robotID); {
				case err == nil:
					mu.Lock()
					wins[taskID]++
					mu.Unlock()
				case !errors.Is(err, fleet.ErrAlreadyAssigned):
					t.Errorf("AssignTask(%s, %s): unexpected error: %v", taskID, robotID, err)
				}
			}(taskID, fmt.Sprintf("agv-%02d", r))
		}
	}
	wg.Wait()

	total := 0
	for taskID, n := range wins {
		if n != 1 {
			t.Errorf("%s: %d winners, want exactly 1", taskID, n)
		}
		total += n
	}
	if total != tasks {
		t.Fatalf("winner total = %d, want %d (one per task)", total, tasks)
	}
	if got := len(core.Snapshot()); got != tasks {
		t.Fatalf("snapshot holds %d assignments, want %d", got, tasks)
	}
	if led.Len() != tasks {
		t.Fatalf("ledger has %d entries, want %d (losers must never reach the WAL)", led.Len(), tasks)
	}
	if broken, err := led.Verify(); broken != -1 || err != nil {
		t.Fatalf("audit chain broken under load at %d: %v", broken, err)
	}
}
