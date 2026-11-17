#!/usr/bin/env bash
# Pillar 3 demo: a three-tier supply-chain payment. Each tier registers what it
# owes the next, the tier above approves, and the buyer's single payment
# credits every tier in one commit. Then the same data is looked at in SQL from
# inside and outside the private collection.
#
# Needs the network up with the supplychain contract deployed
# (tools/deploy-contracts.sh) and the services built into ./bin.
# Run from the services folder.
set -u
cd "$(dirname "$0")"
mkdir -p logs

say() { printf '\n\033[1;34m== %s\033[0m\n' "$*"; }
sql() { # container, query
  docker exec "$1" bash -c "bin/ysqlsh -h \$(hostname) -U yugabyte -tA -c \"$2\"" 2>&1
}

PIDS=()
cleanup() { kill "${PIDS[@]}" 2>/dev/null; wait 2>/dev/null; }
trap cleanup EXIT

# The bank systems, so the released payment can actually be credited.
if ! curl -sf localhost:9101/admin/state > /dev/null; then
  ./bin/banksim > logs/banksim.log 2>&1 & PIDS+=($!)
  sleep 0.5
fi

./bin/scdemo | tee logs/scdemo.log
[ "${PIPESTATUS[0]}" -eq 0 ] || { echo "DEMO FAILED" >&2; exit 1; }
CHAIN=$(grep -o 'PO-[0-9]*' logs/scdemo.log | head -n 1)

say "The same chain in SQL: an org inside the private collection (Bank B's database)"
echo "tables holding supplychain data:"
sql yugabyte.bankb.clearchain.com "select '   ' || table_schema || '.' || table_name from information_schema.tables where table_name like 'supplychain%' order by 1"
echo "public obligations of $CHAIN:"
sql yugabyte.bankb.clearchain.com "select '   ' || (db_value->>'obligationId') || '  level ' || (db_value->>'level') || '  ' || (db_value->>'debtorId') || ' -> ' || (db_value->>'creditorId') || '  ' || (db_value->>'status') from mychannel.supplychain where db_value->>'docType'='obligation' and db_value->>'chainId'='$CHAIN' order by 1"

say "The same chain in SQL: an org outside the collection (the Switch's database)"
echo "tables holding supplychain data:"
sql yugabyte.switch.clearchain.com "select '   ' || table_schema || '.' || table_name from information_schema.tables where table_name like 'supplychain%' order by 1"
echo "rows anywhere in the Switch's public supplychain table that mention an amount or invoice reference:"
sql yugabyte.switch.clearchain.com "select '   ' || count(*) from mychannel.supplychain where db_value::text like '%amount%' or db_value::text like '%INV-%'"
echo
echo "Pillar 3 demo complete."
