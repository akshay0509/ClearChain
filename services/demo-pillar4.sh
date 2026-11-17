#!/usr/bin/env bash
# Pillar 4 demo: trace fraud money across banks, hold only the disputed
# amounts, refuse holds without a legal basis or that duplicate another, split
# recovered money pro rata among the victims, and keep case details private.
#
# Needs the network up with the fraudhold contract deployed
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

if ! curl -sf localhost:9101/admin/state > /dev/null; then
  ./bin/banksim > logs/banksim.log 2>&1 & PIDS+=($!)
  sleep 0.5
fi

./bin/fraudemo | tee logs/fraudemo.log
[ "${PIPESTATUS[0]}" -eq 0 ] || { echo "DEMO FAILED" >&2; exit 1; }
RUN=$(grep -o '^RUN=[0-9]*' logs/fraudemo.log | cut -d= -f2)

say "The holds in SQL, in Bank B's own database"
sql yugabyte.bankb.clearchain.com "select '   ' || (db_value->>'holdId') || '  ' || (db_value->>'complaintId') || '  Rs ' || round((db_value->>'amount')::bigint / 100.0, 2) || '  ' || (db_value->>'legalProvision') || '  ' || (db_value->>'status') from mychannel.fraudhold where db_value->>'docType' = 'hold' and db_value->>'holdId' like 'HOLD-${RUN}-%' order by 1"

say "What Bank B's database holds of the victims' case details"
echo "tables:"
sql yugabyte.bankb.clearchain.com "select '   ' || table_schema || '.' || table_name from information_schema.tables where table_name like 'fraudhold%' order by 1"
echo "rows in Bank B's fraudhold tables that mention either victim's name:"
sql yugabyte.bankb.clearchain.com "select '   ' || count(*) from mychannel.fraudhold where db_value::text like '%Kumar%' or db_value::text like '%Devi%'"
echo
echo "Pillar 4 demo complete."
