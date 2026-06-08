// Package allocator implements demo ① — collision-free concurrent task
// allocation. Many tasks arrive concurrently; the allocator guarantees that no
// two robots are ever assigned the same task and no task is dropped, even under
// simultaneous assignment requests.
//
// Reliability property to prove: with N goroutines racing to claim tasks, the
// set of (task -> robot) assignments is a valid matching — each task assigned
// at most once. This is the concurrency-control analogue of the exactly-once
// guarantee from the fintech ledger background.
package allocator

import "errors"

// ErrTaskTaken is returned when a task has already been claimed.
var ErrTaskTaken = errors.New("allocator: task already assigned")

// Assignment records that a task was granted to a robot.
type Assignment struct {
	TaskID  string
	RobotID string
}

// Allocator hands out tasks to robots without double-assignment.
//
// TODO(demo①): back this with a mutex-guarded claim map (baseline) and then a
// lock-free CAS variant; prove correctness with a -race stress test that fires
// many concurrent Claim calls for the same task and asserts exactly one winner.
type Allocator interface {
	// Claim atomically assigns taskID to robotID, or returns ErrTaskTaken.
	Claim(taskID, robotID string) (Assignment, error)
	// Assignments returns the current matching.
	Assignments() []Assignment
}
