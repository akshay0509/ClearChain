#!/usr/bin/env bash
# Pillar 1 demo: a bank degrades, the switch and the network operator agree to
# flag it, every switch's cache updates, payments fail fast before any debit,
# and after a cool-down the bank is probed and returned to healthy.
#
# Needs the network up with the contracts deployed (tools/deploy-contracts.sh)
# and the services built into ./bin. Run from the services folder.
#
# The switches run with -settlement=false here so this demo shows routing on
# its own; demo-pillar2.sh covers what happens to each payment afterwards.
set -u
cd "$(dirname "$0")"
mkdir -p logs
RATE=${RATE:-20}

say() { printf '\n\033[1;34m== %s\033[0m\n' "$*"; }
flag_of() { curl -s "$1/state" | jq -r --arg b "$2" '.flags[] | select(.bankId==$b) | .status'; }
wait_flag() { # url bank status timeout-seconds
  for _ in $(seq 1 $(($4 * 5))); do
    [ "$(flag_of "$1" "$2")" = "$3" ] && return 0
    sleep 0.2
  done
  echo "TIMED OUT waiting for $2 to become $3 at $1" >&2
  return 1
}

PIDS=()
cleanup() { kill "${PIDS[@]}" 2>/dev/null; wait 2>/dev/null; }
trap cleanup EXIT

say "One-time setup: register the banks"
./bin/setup || exit 1

say "Starting bank systems, operator hop and two switches"
./bin/banksim > logs/banksim.log 2>&1 & PIDS+=($!)
sleep 0.5
./bin/operator > logs/operator.log 2>&1 & PIDS+=($!)
./bin/switch -name switch-1 -addr :9100 -settlement=false > logs/switch-1.log 2>&1 & PIDS+=($!)
./bin/switch -name switch-2 -addr :9110 -propose=false -settlement=false > logs/switch-2.log 2>&1 & PIDS+=($!)
for url in http://localhost:9100 http://localhost:9110; do
  for _ in $(seq 1 50); do curl -sf "$url/state" > /dev/null && break; sleep 0.2; done
done
curl -s -X POST localhost:9101/admin/bank/BANKB/mode -d '{"mode":"healthy"}' > /dev/null
echo "switch-1 sees BANKB: $(flag_of http://localhost:9100 BANKB)   switch-2 sees BANKB: $(flag_of http://localhost:9110 BANKB)"
if [ "$(flag_of http://localhost:9100 BANKB)" != "HEALTHY" ]; then
  echo "BANKB must start HEALTHY for this demo; wait for it to recover or redeploy." >&2
  exit 1
fi

say "Sending $RATE payments/second from BANKA customers to BANKB merchants"
./bin/loadgen -rate "$RATE" -duration 130s > logs/loadgen.log 2>&1 & LOADGEN=$!
sleep 10
tail -n 3 logs/loadgen.log

say "BANKB's core system starts struggling"
curl -s -X POST localhost:9101/admin/bank/BANKB/mode -d '{"mode":"degraded"}'; echo
T_DEGRADE=$(date +%s.%N)
wait_flag http://localhost:9100 BANKB DEGRADED 60 || exit 1
T_FLAG=$(date +%s.%N)
wait_flag http://localhost:9110 BANKB DEGRADED 5 || exit 1
printf 'flag committed and cached %.1fs after the bank began to struggle\n' "$(echo "$T_FLAG - $T_DEGRADE" | bc)"
echo "payments in that gap (before the flag):"
tail -n 12 logs/loadgen.log | sed 's/^/    /'

say "How quickly each switch's cache picked up the committed flag"
curl -s localhost:9100/state | jq -r '.proposals.BANKB | "switch-1 (proposer): commit returned \(.committedAt), cache updated \(.cacheUpdatedAt), lag \(.cacheLagMs) ms\n  reason on ledger: \(.reason)"'
C1=$(curl -s localhost:9100/state | jq -r '.proposals.BANKB.committedAt')
U2=$(curl -s localhost:9110/state | jq -r '.flags[] | select(.bankId=="BANKB") | .updatedAt')
echo "switch-2 (listener): cache updated $U2"
echo "switch-2 lag after commit: $(( ( $(date -d "$U2" +%s%N) - $(date -d "$C1" +%s%N) ) / 1000000 )) ms"

say "The next payment to BANKB, through each switch"
for port in 9100 9110; do
  RRN="DEMO${port}$(date +%s)"
  curl -s -X POST localhost:$port/pay -d "{\"rrn\":\"$RRN\",\"payerBank\":\"BANKA\",\"payeeBank\":\"BANKB\",\"amount\":25000}" \
    | jq -c '{rrn, outcome, reason, debited, payeeFlag, decisionMicros, totalMs}'
  echo "  payer bank's own record for $RRN: $(curl -s localhost:9101/bank/BANKA/payment/$RRN)"
done

say "BANKB's core system recovers; the flag stays DEGRADED through the cool-down"
curl -s -X POST localhost:9101/admin/bank/BANKB/mode -d '{"mode":"healthy"}'; echo
wait_flag http://localhost:9100 BANKB PROBING 60 || exit 1
echo "cool-down over: BANKB is PROBING, a small share of live payments is let through"
wait_flag http://localhost:9100 BANKB HEALTHY 60 || exit 1
wait_flag http://localhost:9110 BANKB HEALTHY 5 || exit 1
echo "probe payments succeeded for both the switch and the operator: BANKB is HEALTHY on both switches"

say "Flag history on the ledger"
curl -s localhost:9100/state | jq -r '.flags[] | "\(.bankId): \(.status) seq \(.seq) mode \(.mode)"'

wait $LOADGEN
say "Load generator summary"
tail -n 4 logs/loadgen.log
say "Routing decision time on switch-1 (cache lookup only, microseconds)"
curl -s localhost:9100/state | jq -c '.decisionMicros'
say "Outcomes on switch-1"
curl -s localhost:9100/state | jq -c '{outcomes, reasons}'
say "What the banks actually did"
curl -s localhost:9101/admin/state | jq -c '.[]'
