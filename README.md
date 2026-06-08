# fleet-master-controller

A VDA5050-compliant fleet master controller, built to demonstrate **operational
reliability for autonomous fleets** — the same distributed-systems guarantees
that matter in financial settlement, applied to coordinating many robots.

> Thesis: keeping *N* independent agents consistent under partial failure is the
> same problem whether the agents move money or move pallets. This project is the
> proof.

## The four reliability demos

| # | Demo | Property proven | Package |
|---|------|-----------------|---------|
| ① | Collision-free concurrent task allocation | No task assigned twice, none dropped, under concurrent claims | `internal/allocator` |
| ② | Exactly-once crash recovery | Restart from the audit log reproduces pre-crash state — no loss, no duplication | `internal/recovery` |
| ③ | **Tamper-evident audit ledger** | Any edit/deletion/reorder of history is detected (hash-chained, append-only) | `internal/ledger` |
| ④ | Graceful dropout reassignment | A robot dropping out → its tasks reclaimed exactly once by healthy robots | `internal/reassign` |

Demo ③ is the differentiator: an append-only, hash-chained audit log where every
control decision is provable after the fact. **It is implemented and tested
now**; ①②④ are scaffolded with their interfaces and the property each must prove.

## Layout

```
cmd/controller/      entry point (runs the ③ ledger demo today)
internal/ledger/     ③ hash-chained append-only audit log  [implemented + tested]
internal/allocator/  ① collision-free concurrent allocation [interface]
internal/recovery/   ② exactly-once replay from the ledger  [interface]
internal/reassign/   ④ dropout reassignment                 [interface]
internal/vda5050/    VDA5050 v2.0 message types + MQTT topic helpers
docs/design.md       reliability properties + roadmap
```

## Run

```bash
go test -race ./...                                   # all reliability properties, race-clean
go run ./cmd/controller assign  /tmp/fleet.wal agv-01 pick-A pick-B
go run ./cmd/controller recover /tmp/fleet.wal        # fresh process rebuilds state from the WAL
go run ./cmd/controller verify  /tmp/fleet.wal        # audit-chain integrity check
```

## Standard

[VDA5050](https://www.vda.de/en/news/publications/publication/vda-5050-v2.0)
v2.0 — the German automotive-industry interface between fleet controllers and
AGVs/AMRs (MQTT + JSON). Modeled subset lives in `internal/vda5050`.

## Status

0-stage vertical slice **done & hardened**: durable WAL = the P3 hash-chain
ledger, P2 exactly-once recovery across a simulated crash (incl. torn-tail and
single-writer handling), P1 single-owner concurrency seed. ①②④ deepen next — see
[`docs/design.md`](docs/design.md). Built as Phase A career-pivot evidence toward
autonomous-fleet orchestration.
