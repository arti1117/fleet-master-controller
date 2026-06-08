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

	"github.com/arti1117/fleet-master-controller/internal/fleet"
	"github.com/arti1117/fleet-master-controller/internal/ledger"
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
	default:
		usage()
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `fleet master controller — vertical slice

  controller assign  <wal> <robotID> <taskID>...   log assignments, then exit
  controller recover <wal>                          rebuild state from the WAL
  controller verify  <wal>                          check audit-chain integrity
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
