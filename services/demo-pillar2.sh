#!/usr/bin/env bash
# Pillar 2 demo: instant merchant settlement tied to a confirmed escrow credit,
# duplicate rejection by RRN, 100 payments at once, auto-reversal of a stuck
# payment, the settlement cycle that repays the float, and SQL reconciliation.
#
# Needs the network up with all contracts deployed (tools/deploy-contracts.sh)
# and the services built into ./bin. Run from the services folder.
set -u
cd "$(dirname "$0")"
mkdir -p logs
TAT=${TAT:-60}
MARGIN=${MARGIN:-15}

say() { printf '\n\033[1;34m== %s\033[0m\n' "$*"; }
L() { ./bin/ledger "$@"; }
float() { L sponsorbank settlement GetFloat | jq -r '.total'; }
rs() { awk -v p="$1" 'BEGIN { printf "Rs %.2f", p / 100 }'; }
wait_until() { # description, timeout seconds, command...
  local what=$1 secs=$2; shift 2
  for _ in $(seq 1 $((secs * 4))); do
    "$@" > /dev/null 2>&1 && return 0
    sleep 0.25
  done
  echo "TIMED OUT waiting for: $what" >&2
  return 1
}
status_is() { [ "$(L switch settlement GetPayment "$1" 2>/dev/null | jq -r '.status')" = "$2" ]; }
fail() { echo "DEMO FAILED: $*" >&2; exit 1; }

PIDS=()
cleanup() { kill "${PIDS[@]}" 2>/dev/null; wait 2>/dev/null; }
trap cleanup EXIT

say "One-time setup: banks, merchants, the deadline rule and the float"
./bin/setup -tat "$TAT" -margin "$MARGIN" || fail "setup"

say "Starting bank systems, operator hop, two switches, bank adapter, aggregator service and watcher"
./bin/banksim -notify http://localhost:9103/credited -healthy-fail-rate 0 > logs/banksim.log 2>&1 & PIDS+=($!)
sleep 0.5
./bin/operator -settlement-file logs/npci-settlement.csv > logs/operator.log 2>&1 & PIDS+=($!)
./bin/bankadapter > logs/bankadapter.log 2>&1 & PIDS+=($!)
./bin/aggregator > logs/aggregator.log 2>&1 & PIDS+=($!)
./bin/watcher > logs/watcher.log 2>&1 & PIDS+=($!)
./bin/switch -name switch-1 -addr :9100 > logs/switch-1.log 2>&1 & PIDS+=($!)
./bin/switch -name switch-2 -addr :9110 -propose=false > logs/switch-2.log 2>&1 & PIDS+=($!)
for url in localhost:9100/state localhost:9110/state localhost:9103/state localhost:9104/state localhost:9105/state; do
  wait_until "$url" 20 curl -sf "$url" || fail "service at $url did not start"
done
FLOAT0=$(float)
echo "float at start: $(rs "$FLOAT0")"

RUN=$(date +%s)
RRN="M${RUN}01"
say "1. A customer pays Raju Electronics Rs 1,000 (RRN $RRN)"
curl -s -X POST localhost:9100/pay -d "{\"rrn\":\"$RRN\",\"payerBank\":\"BANKA\",\"payeeBank\":\"SPONSORBANK\",\"amount\":100000,\"merchantId\":\"M001\"}" \
  | jq -c '{rrn, outcome, debited, totalMs}'
wait_until "payout of $RRN" 30 status_is "$RRN" PAID_OUT || fail "payment $RRN was not paid out"
L aggregator settlement GetPayout "$RRN" | jq -r '"payout on the ledger: gross Rs \(.gross/100)  = merchant Rs \(.merchantAmount/100) + aggregator fee Rs \(.fee/100) + GST Rs \(.gst/100)   (float lane \(.lane))"'
L switch settlement GetPayment "$RRN" | jq -r '"ledger timestamps: debited \(.debitedAt), escrow credit confirmed \(.confirmedAt), merchant paid out \(.paidOutAt)  -> status \(.status)"'
wait_until "bank payout" 10 sh -c "curl -s localhost:9101/bank/SPONSORBANK/payment/$RRN | jq -e '.paidOut' " || fail "sponsor bank did not pay the merchant"
echo "sponsor bank's own record: $(curl -s localhost:9101/bank/SPONSORBANK/payment/$RRN)"
curl -s localhost:9103/state | jq -r '"from the escrow credit landing to the merchant being paid: \(.creditToMerchantPaidMs.max | floor) ms"'
echo "float now: $(rs "$(float)") (down by the merchant amount only)"

say "2. A payout is refused until the sponsor bank has confirmed the escrow credit"
# A payment recorded on the ledger but not yet confirmed: take the bank's confirmation path away briefly.
curl -s -X POST localhost:9101/admin/bank/SPONSORBANK/mode -d '{"mode":"down"}' > /dev/null
STUCK="S${RUN}01"
curl -s -X POST localhost:9100/pay -d "{\"rrn\":\"$STUCK\",\"payerBank\":\"BANKA\",\"payeeBank\":\"SPONSORBANK\",\"amount\":250000,\"merchantId\":\"M001\"}" \
  | jq -c '{rrn, outcome, reason, debited}'
curl -s -X POST localhost:9101/admin/bank/SPONSORBANK/mode -d '{"mode":"healthy"}' > /dev/null
T_STUCK=$(date +%s)
wait_until "record of $STUCK" 20 status_is "$STUCK" DEBITED || fail "stuck payment was not recorded"
L switch settlement GetPayment "$STUCK" | jq -r '"on the ledger: status \(.status), debited at \(.earliestAt), deadline \(.deadlineAt) (\(.deadlineAt - .earliestAt)s later)"'
echo "the aggregator asks for a payout anyway:"
curl -s -X POST "localhost:9104/payout/$STUCK" | grep -o 'payment [^"]*confirmed first' | head -n 1 | grep . | sed 's/^/   refused: /' || fail "unconfirmed payout was not refused"
echo "the watcher's request to reverse it now, well before the deadline:"
L -submit npci settlement RequestReversal "$STUCK" 2>&1 | grep -o 'too early[^"|]*' | head -n 1 | grep . | sed 's/^/   refused: /' || fail "early reversal was not refused"

say "3. The same RRN arrives again"
echo "at the same switch:"
curl -s -X POST localhost:9100/pay -d "{\"rrn\":\"$RRN\",\"payerBank\":\"BANKA\",\"payeeBank\":\"SPONSORBANK\",\"amount\":100000,\"merchantId\":\"M001\"}" | jq -c '{rrn, outcome, reason, debited}'
echo "at a different switch, which has never seen it:"
curl -s -X POST localhost:9110/pay -d "{\"rrn\":\"$RRN\",\"payerBank\":\"BANKA\",\"payeeBank\":\"SPONSORBANK\",\"amount\":100000,\"merchantId\":\"M001\"}" | jq -c '{rrn, outcome}'
wait_until "ledger refusing the duplicate" 20 sh -c "curl -s localhost:9110/state | jq -e '.settlement.duplicatesRefusedByLedger >= 1'" || fail "ledger did not refuse the duplicate record"
echo "   the chaincode refused to record it a second time: $(curl -s localhost:9110/state | jq -c '.settlement')"
echo "a second payout request for it:"
curl -s -X POST "localhost:9104/payout/$RRN" | grep -o 'duplicate[^"|]*' | head -n 1 | grep . | sed 's/^/   refused: /' || fail "duplicate payout was not refused"
L aggregator settlement GetPayment "$RRN" | jq -r '"still one payment, status \(.status), paid out once at \(.paidOutAt)"'

say "4. 100 merchant payments at the same instant"
BEFORE=$(curl -s localhost:9104/state | jq '.payoutsCommitted')
FLOAT1=$(float)
./bin/loadgen -burst 100 -payee SPONSORBANK -merchant M002 -prefix "B${RUN}" | sed 's/^/   /'
T0=$(date +%s.%N)
wait_until "100 payouts" 120 sh -c "[ \$(curl -s localhost:9104/state | jq '.payoutsCommitted') -ge $((BEFORE + 100)) ]" || fail "not all 100 payouts committed: $(curl -s localhost:9104/state)"
printf '   all 100 recorded, confirmed and paid out %.1fs after the last switch response\n' "$(echo "$(date +%s.%N) - $T0" | bc)"
curl -s localhost:9104/state | jq -r '"   aggregator: \(.payoutsCommitted) payouts committed, \(.payoutsRefused) refused, \(.duplicatesRefused) duplicates, \(.laneConflictRetries) resubmitted after a float-lane conflict"'
curl -s localhost:9103/state | jq -r '"   escrow credit to merchant paid: median \(.creditToMerchantPaidMs.p50 | floor) ms, 95th percentile \(.creditToMerchantPaidMs.p95 | floor) ms, slowest \(.creditToMerchantPaidMs.max | floor) ms"'
docker exec yugabyte.sponsorbank.clearchain.com bash -c "bin/ysqlsh -h \$(hostname) -U yugabyte -tA -c \"select 'ledger, by SQL: ' || count(*) || ' payouts for ' || count(distinct db_value->>'rrn') || ' distinct RRNs, Rs ' || round(sum((db_value->>'merchantAmount')::bigint)/100.0, 2) || ' paid to merchants' from mychannel.settlement where db_value->>'docType'='payout' and db_value->>'rrn' like 'B${RUN}%'\"" | sed 's/^/   /'
PAID=$(docker exec yugabyte.sponsorbank.clearchain.com bash -c "bin/ysqlsh -h \$(hostname) -U yugabyte -tA -c \"select sum((db_value->>'merchantAmount')::bigint) from mychannel.settlement where db_value->>'docType'='payout' and db_value->>'rrn' like 'B${RUN}%'\"")
FLOAT2=$(float)
echo "   float fell by $(rs $((FLOAT1 - FLOAT2))); the 100 payouts total $(rs "$PAID")"
[ $((FLOAT1 - FLOAT2)) -eq "$PAID" ] || fail "float movement does not equal the payouts"

say "5. The stuck payment $STUCK reaches its reversal window (${TAT}s deadline, opens ${MARGIN}s before)"
echo "watcher is tracking: $(curl -s localhost:9105/state | jq -c '{unconfirmed, rule}')"
wait_until "auto-reversal" $((TAT + 30)) status_is "$STUCK" REVERSED || fail "stuck payment was not reversed"
L npci settlement GetReversal "$STUCK" | jq -r '"reversed on the ledger \(.reversedAt - .earliestAt)s after the debit, \(.secondsBeforeDeadline)s before the deadline; within deadline: \(.withinDeadline); compensation owed: Rs \(.compensationPaise/100); float advanced: \(.floatAdvanced); requested by \(.requestedBy)"'
wait_until "payer bank reversal" 10 sh -c "curl -s localhost:9101/bank/BANKA/payment/$STUCK | jq -e '.reversed'" || fail "payer bank did not reverse"
echo "payer bank's own record: $(curl -s localhost:9101/bank/BANKA/payment/$STUCK)"
echo "a payout for it is impossible: $(curl -s -X POST "localhost:9104/payout/$STUCK" | grep -o 'payment [^"]*confirmed first' | head -n 1)"
[ "$(float)" -eq "$FLOAT2" ] || fail "the reversal touched the float"
echo "float unchanged by the reversal: $(rs "$(float)")"

say "6. The aggregator's settlement cycle releases escrow and repays the float"
curl -s -X POST localhost:9103/cycle -d "{\"cycleId\":\"CYCLE-${RUN}\"}" | jq -r '.[] | fromjson? // . | "cycle \(.cycleId): \(.count) payouts, Rs \(.totalReturned/100) back to the float, aggregator keeps fee Rs \(.totalFee/100) + GST Rs \(.totalGst/100)"'
FLOAT3=$(float)
echo "float after the cycle: $(rs "$FLOAT3")"
[ "$FLOAT3" -eq "$FLOAT0" ] || fail "float is not whole after the cycle (start $FLOAT0, now $FLOAT3)"
echo "float is back to where it started: the advance was a short-term funding cost, not a loss"

say "7. Reconciliation against the operator's settlement file, in SQL"
./recon.sh logs/npci-settlement.csv

say "Summary"
echo "switch-1: $(curl -s localhost:9100/state | jq -c '{outcomes, settlement}')"
echo "banks:    $(curl -s localhost:9101/admin/state | jq -c '.')"
echo
echo "Pillar 2 demo complete."
