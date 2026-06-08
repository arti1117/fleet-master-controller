# Design notes — fleet-master-controller

## Why this exists

Career-pivot evidence (Phase A): the single biggest gap is a *fleet controller
deliverable*. This project closes it by proving four reliability properties that
transfer directly from financial settlement systems to robot-fleet
orchestration. Built in Go to read like the control-plane world it targets
(single-owner goroutine + channels, WAL, reconcile loop).

## Core invariant

> Each task is assigned **exactly once** at any moment, the assignment history
> is **tamper-evident**, and the system **recovers without loss or duplication**
> across both process crashes (②) and robot dropouts (④).

Demo ① (concurrency control) and ② (exactly-once recovery) are the same two
guarantees as fintech exactly-once processing, re-expressed for tasks. Demo ③
(hash-chained ledger) is the auditable record that makes both provable after the
fact. Demo ④ is the fleet-specific failure mode (Kubernetes-style reconcile).

## 0-stage vertical slice — DONE (2026-06-08)

Robot + task + durable WAL + crash recovery, one full pass, hardened after an
adversarial review. What it proves today:

- **P3** hash-chained append-only ledger; interior tamper/reorder detected by `Verify`.
- **P2** exactly-once recovery: a fresh process rebuilds the exact assignment set from the WAL.
- **P1 seed** single goroutine owns the assignment map; 32-racer contended task → exactly one winner (`-race` clean).
- Collision-free assignment; `ErrAlreadyAssigned` / `ErrClosed` sentinels.

### Crash semantics (implemented — the subtle part)

- **Torn trailing record** (mid-write power loss): the canonical crash shape.
  Replay drops the unterminated tail and truncates the file to the last complete
  record; every prior fsync'd (acknowledged) record still recovers. A torn tail
  must never brick recovery.
- **Interior corruption** (a complete record that fails to parse or breaks the
  hash chain): real tamper/damage → `Open` fails hard.
- **fsync failure**: durability is indeterminate (bytes may be on disk), so the
  ledger **poisons itself and fails-stop** rather than risk index/prev-hash
  drift across later appends. A returned Append error means "not committed,
  outcome possibly indeterminate" — treat as fail-stop, not retry.
- **Single writer**: the WAL is `flock`'d; a second process opening it fails.
- **Directory durability**: the parent directory is fsync'd so a freshly created
  WAL survives a crash.

### Ledger / WAL design

- Append-only; entries never mutated in place.
- `Hash = sha256(Index | Timestamp(RFC3339Nano) | Kind | Payload | PrevHash)`.
- `PrevHash` links each entry to its predecessor → reordering/deletion breaks the chain.
- Caller supplies the timestamp (no hidden clock) so replay is deterministic.
- The WAL **is** the audit ledger: P2 recovery and P3 audit are one artifact.

## Roadmap

1. **[done]** 0-stage slice: durable WAL + recovery + P3 ledger, hardened.
2. **[next]** P2 deepen: snapshot + compaction so recovery isn't O(history); explicit "kill -9 mid-order" demo.
3. P1: contended allocation under load, `-race` stress; document the single-owner proof.
4. P4: reconcile loop (desired vs actual) — robot dropout via VDA5050 connection/last-will → re-`Append` "reassign"; kill-a-robot conservation test.
5. VDA5050 MQTT transport (`internal/vda5050` ↔ broker), end-to-end with simulated AGVs.

## Documented limitations (deliberately deferred — not bugs in the slice)

- **No WAL compaction yet**: `Open` replays the whole history into memory. Fine
  for the slice; snapshotting is roadmap item 2.
- **`Snapshot` on a closed Core returns nil** (vs an explicit ok/closed signal).
  Acceptable for the CLI, which only snapshots live cores.
- **`run()` goroutine requires `Close`**: leaking it is a caller contract
  violation, not a defect; `Close` is idempotent.
- **`flock` is Unix-only**: acceptable for a Linux-targeted control plane.
- **fsync-failure residual**: a Sync error after a successful Write leaves the
  record possibly-durable; recovery treats a present, complete record as
  committed. The fail-stop + indeterminate contract is the resolution, not a
  perfect 2-phase commit — that needs the orderId/orderUpdateId idempotency keys
  on the robot side (modeled in `internal/vda5050`).

## Open decisions

- Persistence/compaction backend (append-only file + periodic snapshot vs embedded KV).
- MQTT library (eclipse/paho vs alternatives).
- Whether to drive the slice from the F1TENTH sim (links to OSS track A) as a live AGV source.
