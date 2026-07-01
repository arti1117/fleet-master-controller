// Package fleet is the controller core. It owns all mutable fleet state in a
// single goroutine; callers interact only by submitting operations over a
// channel (Go's "share memory by communicating"). This makes concurrent task
// handling (demo ①) race-free by construction, and the construction is
// compiler-enforced: the assignment map exists only as a local variable of the
// owner goroutine (run), so no other code can even name it. No locks guard the
// map, because no second goroutine can reach it.
//
// Every accepted assignment is durably appended to the ledger/WAL before it is
// reflected in memory, so a crash leaves the WAL as the single source of truth
// and recovery (demo ②) rebuilds the exact pre-crash state.
package fleet

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/arti1117/fleet-master-controller/internal/event"
	"github.com/arti1117/fleet-master-controller/internal/ledger"
	"github.com/arti1117/fleet-master-controller/internal/recovery"
)

// Sentinel errors callers can match with errors.Is.
var (
	// ErrClosed is returned when an operation is submitted to a closed Core.
	ErrClosed = errors.New("fleet: core is closed")
	// ErrAlreadyAssigned is returned when a task is already held by a robot.
	ErrAlreadyAssigned = errors.New("fleet: task already assigned")
)

// Clock returns the timestamp recorded for an event. Injectable so tests get
// deterministic ledger hashes; production uses time.Now.
type Clock func() time.Time

// Core is the single-owner fleet state machine.
type Core struct {
	led       *ledger.Ledger
	clock     Clock
	ops       chan func(assignments map[string]string) // executed on the owner goroutine
	quit      chan struct{}                            // closed by Close to stop the owner goroutine
	stopped   chan struct{}                            // closed by run when it exits
	closeOnce sync.Once

	// The assignment map is deliberately NOT a field here: it lives as a local
	// of run(), so ownership by that goroutine is enforced by the compiler.
}

// Option configures a Core.
type Option func(*Core)

// WithClock overrides the event clock (tests use a fixed clock).
func WithClock(c Clock) Option { return func(core *Core) { core.clock = c } }

// Open builds a Core whose state is recovered from led (which has already
// replayed its on-disk WAL), then starts the owner goroutine.
func Open(led *ledger.Ledger, opts ...Option) (*Core, error) {
	st, err := recovery.Replay(led.Entries())
	if err != nil {
		return nil, err
	}
	c := &Core{
		led:     led,
		clock:   func() time.Time { return time.Now().UTC() },
		ops:     make(chan func(map[string]string)),
		quit:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	for _, o := range opts {
		o(c)
	}
	go c.run(st.Assignments)
	return c, nil
}

// run is the owner goroutine. The assignment map (taskID -> robotID) enters as
// its argument and never leaves: it is not a Core field, so no other method can
// even name it — single ownership is enforced by the compiler, not by locks or
// discipline. The only way to touch fleet state is to send an op here, and ops
// execute one at a time on this goroutine.
func (c *Core) run(assignments map[string]string) {
	defer close(c.stopped)
	for {
		select {
		case op := <-c.ops:
			op(assignments)
		case <-c.quit:
			return
		}
	}
}

// submit runs fn on the owner goroutine and returns fn's error, or ErrClosed if
// the Core has been closed. This is the one place caller goroutines hand work
// to the owner; everything mutable is touched only inside fn, on that goroutine.
func (c *Core) submit(fn func(assignments map[string]string) error) error {
	reply := make(chan error, 1)
	select {
	case c.ops <- func(m map[string]string) { reply <- fn(m) }:
		return <-reply
	case <-c.quit:
		return ErrClosed
	}
}

// AssignTask grants taskID to robotID. It is collision-free: a task already
// assigned is rejected with ErrAlreadyAssigned (demo ①). The assignment is
// durably logged before it is acknowledged (demo ②/③); a ledger error is
// propagated and the in-memory state is left untouched.
func (c *Core) AssignTask(taskID, robotID string) error {
	return c.submit(func(assignments map[string]string) error {
		if cur, exists := assignments[taskID]; exists {
			return fmt.Errorf("%w: %q held by %q", ErrAlreadyAssigned, taskID, cur)
		}
		payload, err := json.Marshal(event.Assign{TaskID: taskID, RobotID: robotID})
		if err != nil {
			return err
		}
		if _, err := c.led.Append(c.clock(), event.KindAssign, payload); err != nil {
			return err // not durably committed -> not assigned (WAL is the source of truth)
		}
		assignments[taskID] = robotID
		return nil
	})
}

// RecordOrder logs that the controller issued a VDA5050 order (update) to a
// robot. It does not change the assignment projection — it feeds the audit
// ledger that internal/reconcile reads to prove command acceptance.
func (c *Core) RecordOrder(robotID, orderID string, updateID uint32, taskID string) error {
	return c.submit(func(map[string]string) error {
		payload, err := json.Marshal(event.OrderIssued{
			RobotID: robotID, OrderID: orderID, OrderUpdateID: updateID, TaskID: taskID,
		})
		if err != nil {
			return err
		}
		_, err = c.led.Append(c.clock(), event.KindOrderIssued, payload)
		return err
	})
}

// RecordState logs a VDA5050 state report from a robot (the fields reconcile needs).
func (c *Core) RecordState(robotID, orderID string, updateID uint32, driving bool) error {
	return c.submit(func(map[string]string) error {
		payload, err := json.Marshal(event.StateReported{
			RobotID: robotID, OrderID: orderID, OrderUpdateID: updateID, Driving: driving,
		})
		if err != nil {
			return err
		}
		_, err = c.led.Append(c.clock(), event.KindStateReported, payload)
		return err
	})
}

// Snapshot returns a copy of the current taskID -> robotID assignments, or nil
// if the Core has been closed.
func (c *Core) Snapshot() map[string]string {
	reply := make(chan map[string]string, 1)
	select {
	case c.ops <- func(assignments map[string]string) {
		m := make(map[string]string, len(assignments))
		for k, v := range assignments {
			m[k] = v
		}
		reply <- m
	}:
		return <-reply
	case <-c.quit:
		return nil
	}
}

// Close stops the owner goroutine. It is idempotent and safe to call
// concurrently. The ledger/WAL is the caller's to close.
func (c *Core) Close() {
	c.closeOnce.Do(func() {
		close(c.quit)
		<-c.stopped
	})
}
