#!/usr/bin/env bash
# Reconcile the network operator's settlement file against the ledger with SQL.
#
# Drunix keeps each chaincode's state in an ordinary table in the org's own
# YugabyteDB: schema = channel, table = chaincode, one JSON document per key.
# So reconciliation is a join, not a file-matching job.
#
#   ./recon.sh logs/npci-settlement.csv [database container]
#
# The settlement file has one line per credit a bank confirmed: rrn,bank,amount
set -eu
FILE=${1:?usage: recon.sh <settlement-file.csv> [database container]}
DB=${2:-yugabyte.npci.clearchain.com}

{
  cat <<'SQL'
\set QUIET on
\pset footer off
create temp table npci_file (rrn text primary key, bank text, amount bigint);
copy npci_file from stdin with (format csv, rows_per_transaction 0);
SQL
  sort -u -t, -k1,1 "$FILE"
  cat <<'SQL'
\.
create temp view ledger as
  select db_value->>'rrn'               as rrn,
         db_value->>'payeeBank'         as bank,
         (db_value->>'amount')::bigint  as amount,
         db_value->>'status'            as status
  from mychannel.settlement
  where db_value->>'docType' = 'payment';

\echo
\echo Ledger payments by status
select status, count(*) as payments, round(sum(amount) / 100.0, 2) as rupees
from ledger group by status order by status;

\echo
\echo Settlement file against the ledger
select
  (select count(*) from npci_file)                                              as lines_in_file,
  (select count(*) from npci_file f join ledger l using (rrn)
     where l.amount = f.amount and l.bank = f.bank
       and l.status in ('CONFIRMED', 'PAID_OUT', 'REIMBURSED'))                 as matched,
  (select count(*) from npci_file f join ledger l using (rrn)
     where l.amount <> f.amount or l.bank <> f.bank)                            as amount_or_bank_differs,
  (select count(*) from npci_file f left join ledger l using (rrn)
     where l.rrn is null)                                                       as in_file_not_on_ledger,
  (select count(*) from ledger l left join npci_file f using (rrn)
     where f.rrn is null and l.status in ('CONFIRMED', 'PAID_OUT', 'REIMBURSED')) as confirmed_on_ledger_not_in_file,
  (select count(*) from npci_file f join ledger l using (rrn)
     where l.status in ('DEBITED', 'REVERSED'))                                 as in_file_but_unconfirmed_on_ledger;

\echo
\echo Never credited, so correctly absent from the file
select l.rrn, l.status, round(l.amount / 100.0, 2) as rupees
from ledger l left join npci_file f using (rrn)
where f.rrn is null and l.status in ('DEBITED', 'REVERSED')
order by l.rrn limit 20;

\echo
\echo Merchant payouts and what the aggregator earned
select count(*) as payouts,
       round(sum((db_value->>'gross')::bigint) / 100.0, 2)          as gross_rupees,
       round(sum((db_value->>'merchantAmount')::bigint) / 100.0, 2) as paid_to_merchants,
       round(sum((db_value->>'fee')::bigint) / 100.0, 2)            as aggregator_fee,
       round(sum((db_value->>'gst')::bigint) / 100.0, 2)            as gst_on_fee
from mychannel.settlement where db_value->>'docType' = 'payout';
SQL
} | docker exec -i "$DB" bash -c 'bin/ysqlsh -h "$(hostname)" -U yugabyte -v ON_ERROR_STOP=1 -f -'
