// Command controller drives the 0-stage vertical slice of the fleet master
// controller: assign tasks (durably logged to a WAL), then recover the exact
// state from that WAL in a fresh process — demonstrating crash recovery (②)
// over a tamper-evident audit ledger (③).
//
// Usage:
//
//	controller assign  <wal> <robotID> <taskID>...   # log assignments, then exit ("crash")
//	controller recover <wal>                         # rebuild state from the WAL, verify chain
//	controller verify  <wal>                         # check audit-chain integrity only
//
// Typical demo:
//
//	go run ./cmd/controller assign  /tmp/fleet.wal agv-01 pick-A pick-B
//	go run ./cmd/controller recover /tmp/fleet.wal   # both tasks recovered, chain intact
package main

import (
	"fmt"
	"os"
	"sort"
	"strconv"

	"github.com/arti1117/fleet-master-controller/internal/fleet"
	"github.com/arti1117/fleet-master-controller/internal/ledger"
	"github.com/arti1117/fleet-master-controller/internal/reconcile"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "assign":
		cmdAssign(os.Args[2:])
	case "recover":
		cmdRecover(os.Args[2:])
	case "verify":
		cmdVerify(os.Args[2:])
	case "record-order":
		cmdRecordOrder(os.Args[2:])
	case "record-state":
		cmdRecordState(os.Args[2:])
	case "reconcile":
		cmdReconcile(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `fleet master controller

  controller assign       <wal> <robotID> <taskID>...            log assignments, then exit
  controller recover      <wal>                                  rebuild state from the WAL
  controller verify       <wal>                                  check audit-chain integrity
  controller record-order <wal> <robot> <orderID> <updateID>     log a VDA5050 order issued to a robot
  controller record-state <wal> <robot> <orderID> <updateID> <driving:true|false>
  controller reconcile    <wal>                                  prove command acceptance (audit layer)
`)
	os.Exit(2)
}

// open returns a recovered Core over the WAL at path, plus the underlying
// ledger so the caller can verify/close it.
func open(path string) (*ledger.Ledger, *fleet.Core) {
	led, err := ledger.Open(path)
	if err != nil {
		fail(err)
	}
	core, err := fleet.Open(led)
	if err != nil {
		fail(err)
	}
	return led, core
}

func cmdAssign(args []string) {
	if len(args) < 3 {
		usage()
	}
	path, robot, tasks := args[0], args[1], args[2:]
	led, core := open(path)
	defer led.Close()
	defer core.Close()

	for _, task := range tasks {
		if err := core.AssignTask(task, robot); err != nil {
			fmt.Printf("  skip %-10s -> %-8s : %v\n", task, robot, err)
			continue
		}
		fmt.Printf("  assigned %-10s -> %-8s (durably logged)\n", task, robot)
	}
	printState("state after assign", core)
	fmt.Println("process exiting (simulated crash — no completion step)")
}

func cmdRecover(args []string) {
	if len(args) != 1 {
		usage()
	}
	led, core := open(args[0])
	defer led.Close()
	defer core.Close()

	fmt.Printf("recovered %d entrie(s) from WAL\n", led.Len())
	if broken, err := led.Verify(); err != nil {
		fmt.Printf("AUDIT FAILED: chain broken at entry %d: %v\n", broken, err)
		os.Exit(1)
	}
	fmt.Println("audit chain: INTACT")
	printState("recovered state", core)
}

func cmdVerify(args []string) {
	if len(args) != 1 {
		usage()
	}
	led, err := ledger.Open(args[0])
	if err != nil {
		fail(err)
	}
	defer led.Close()
	if broken, err := led.Verify(); err != nil {
		fmt.Printf("INVALID: chain broken at entry %d: %v\n", broken, err)
		os.Exit(1)
	}
	fmt.Printf("VALID: %d entrie(s), chain intact\n", led.Len())
}

func cmdRecordOrder(args []string) {
	if len(args) != 4 {
		usage()
	}
	path, robot, orderID := args[0], args[1], args[2]
	upd := parseUpdateID(args[3])
	led, core := open(path)
	defer led.Close()
	defer core.Close()
	if err := core.RecordOrder(robot, orderID, upd, ""); err != nil {
		fail(err)
	}
	fmt.Printf("logged order issued: %s -> %s orderUpdateId=%d\n", orderID, robot, upd)
}

func cmdRecordState(args []string) {
	if len(args) != 5 {
		usage()
	}
	path, robot, orderID := args[0], args[1], args[2]
	upd := parseUpdateID(args[3])
	driving, err := strconv.ParseBool(args[4])
	if err != nil {
		fail(fmt.Errorf("driving must be true|false: %w", err))
	}
	led, core := open(path)
	defer led.Close()
	defer core.Close()
	if err := core.RecordState(robot, orderID, upd, driving); err != nil {
		fail(err)
	}
	fmt.Printf("logged state: %s @ %s orderUpdateId=%d driving=%v\n", robot, orderID, upd, driving)
}

func cmdReconcile(args []string) {
	if len(args) != 1 {
		usage()
	}
	led, err := ledger.Open(args[0])
	if err != nil {
		fail(err)
	}
	defer led.Close()

	findings, err := reconcile.Reconcile(led.Entries())
	if err != nil {
		fail(err)
	}
	if len(findings) == 0 {
		fmt.Println("no orders issued — nothing to reconcile")
		return
	}
	fmt.Printf("command-state reconciliation (%d order(s)):\n", len(findings))
	for _, f := range findings {
		fmt.Printf("  [%-8s] %s @ %s — %s\n", f.Status, f.RobotID, f.OrderID, f.Detail)
	}
	counts := reconcile.CountByStatus(findings)
	if attention := counts[reconcile.StatusStalled] + counts[reconcile.StatusUnobserved]; attention > 0 {
		fmt.Printf("ATTENTION: %d unconfirmed (%d stalled, %d unobserved)\n",
			attention, counts[reconcile.StatusStalled], counts[reconcile.StatusUnobserved])
		os.Exit(1)
	}
	fmt.Println("OK: every issued order confirmed accepted")
}

func parseUpdateID(s string) uint32 {
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		fail(fmt.Errorf("orderUpdateId must be a uint32: %w", err))
	}
	return uint32(v)
}

func printState(label string, core *fleet.Core) {
	snap := core.Snapshot()
	tasks := make([]string, 0, len(snap))
	for t := range snap {
		tasks = append(tasks, t)
	}
	sort.Strings(tasks)
	fmt.Printf("%s (%d task(s)):\n", label, len(tasks))
	for _, t := range tasks {
		fmt.Printf("  %-10s -> %s\n", t, snap[t])
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
