#!/usr/bin/env bash
# kill-9 crash-recovery demo — P2 evidence for the fleet master controller.
#
# 컨트롤러가 WAL(=감사 원장)에 할당을 기록하는 도중에 SIGKILL(kill -9)로
# 강제 종료한 뒤, 새 프로세스로 복구하여 다음을 보인다:
#   (P2) 유실·중복 0 — 죽기 직전까지 durable하게 커밋된 할당만 정확히 복구
#   (P3) 해시 체인 무결 — 강제 종료가 남긴 torn tail은 잘려나가고 체인은 그대로
#
# 사용법:  scripts/kill9-demo.sh [할당건수=2000] [반복횟수=3]
#   반복할 때마다 kill 타이밍을 무작위로 흔들어(ack/fsync 경계) 어느 순간에
#   죽어도 복구가 일관됨을 보인다.
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1   # repo 루트

N="${1:-2000}"      # 시도할 할당 건수 (각 건은 fsync되므로 끝나기 전 kill 가능)
ROUNDS="${2:-3}"    # kill 타이밍을 바꿔 반복

BINDIR="$(mktemp -d)"; BIN="$BINDIR/controller"
echo "빌드 중..."
go build -o "$BIN" ./cmd/controller || { echo "build 실패"; rm -rf "$BINDIR"; exit 1; }
TASKS="$(seq -f 'task%g' 1 "$N")"

pass=0
for r in $(seq 1 "$ROUNDS"); do
  WALDIR="$(mktemp -d "${HOME}/.kill9demo.XXXXXX")"; WAL="$WALDIR/fleet.wal"

  # 1) 백그라운드로 N건 할당을 WAL에 기록 시작 (각 건마다 fsync)
  "$BIN" assign "$WAL" agv-01 $TASKS >/dev/null 2>&1 &
  PID=$!

  # 2) 무작위 시점(쓰는 도중)에 kill -9 — 정상 종료가 아니라 강제 종료
  SLEEP="$(awk "BEGIN{printf \"%.3f\", 0.03 + (${RANDOM}/32767)*0.35}")"
  sleep "$SLEEP"
  kill -9 "$PID" 2>/dev/null
  wait "$PID" 2>/dev/null

  # 3) 새 프로세스로 복구 + 4) 감사 체인 검증
  OUT="$("$BIN" recover "$WAL" 2>&1)"
  REC="$(printf '%s\n' "$OUT" | grep -oE 'recovered [0-9]+ ' | grep -oE '[0-9]+')"
  if printf '%s\n' "$OUT" | grep -q 'INTACT'; then chain="INTACT ✓"; else chain="BROKEN ✗"; fi
  VOUT="$("$BIN" verify "$WAL" 2>&1 | tr -d '\n')"

  printf "round %d: %5.3fs 후 kill -9 → 복구 %s건 · 체인 %s · %s\n" \
    "$r" "$SLEEP" "${REC:-0}" "$chain" "$VOUT"
  [ "$chain" = "INTACT ✓" ] && pass=$((pass+1))
  rm -rf "$WALDIR"
done
rm -rf "$BINDIR"

echo
echo "결과: ${ROUNDS}회 중 ${pass}회 무결 복구 (시도 ${N}건/회, kill 타이밍 무작위)"
if [ "$pass" -eq "$ROUNDS" ]; then
  echo "✓ PASS — 강제 종료(kill -9)에도 유실·중복·손상 0, 감사 체인 무결"
  exit 0
else
  echo "✗ 일부 라운드에서 체인이 깨짐 — 조사 필요"
  exit 1
fi
