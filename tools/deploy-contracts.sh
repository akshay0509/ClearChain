#!/usr/bin/env bash
# Deploy ClearChain's contracts with their endorsement policies.
#
#   tools/deploy-contracts.sh                 deploy all three
#   tools/deploy-contracts.sh settlement      deploy one
#
# Each contract is installed only on the lite peers of the orgs that endorse it
# or read it directly. Org numbers: 1 switch, 2 npci, 3 banka, 4 bankb,
# 5 aggregator, 6 sponsorbank, 7 buyer, 8 investigator.
#
# Needs the network up with the channel created, and the Drunix binaries on PATH.
set -u
cd "$(dirname "$0")/../clearchain-test-network" || exit 1

deploy() { # name, install orgs, policy, [collections config]
  local name=$1 orgs=$2 policy=$3 collections=${4:-}
  local args=(deployCC -ccn "$name" -ccp "../chaincode/$name" -ccl go -ccep "$policy")
  if [ -n "$collections" ]; then
    args+=(-cccg "$collections")
  fi
  echo "== Deploying $name (installed on orgs: $orgs; policy: $policy)"
  if ! CC_INSTALL_ORGS="$orgs" ./network.sh "${args[@]}" > "/tmp/deploy-$name.log" 2>&1; then
    echo "   FAILED - see /tmp/deploy-$name.log"
    tail -n 5 "/tmp/deploy-$name.log"
    return 1
  fi
  grep -a 'Approvals:' "/tmp/deploy-$name.log" | tail -n 1 | sed 's/^/   /'
}

want=${1:-all}
rc=0
if [ "$want" = all ] || [ "$want" = healthflag ]; then
  # A flag change needs the switch that measured it and the network operator.
  deploy healthflag "1 2" "AND('SwitchOrgMSP.peer','NPCIOrgMSP.peer')" || rc=1
fi
if [ "$want" = all ] || [ "$want" = settlement ]; then
  # A merchant payout needs the aggregator and its sponsor bank, which hold the
  # escrow and the float. The switch and the operator read it, so they have it too.
  deploy settlement "1 2 5 6" "AND('AggregatorOrgMSP.peer','SponsorBankOrgMSP.peer')" || rc=1
fi
if [ "$want" = all ] || [ "$want" = supplychain ]; then
  # An obligation needs the orgs its parties act through: the buyer's and the
  # suppliers' banks. Amounts stay in a private collection shared by those three.
  deploy supplychain "3 4 7" "AND('BuyerOrgMSP.peer','BankAOrgMSP.peer','BankBOrgMSP.peer')" \
    ../chaincode/supplychain/collections_config.json || rc=1
fi
if [ "$want" = all ] || [ "$want" = fraudhold ]; then
  # A hold needs the investigating authority and one bank. The contract itself
  # refuses to run on any bank's peer other than the one the action concerns.
  deploy fraudhold "3 4 6 8" "AND('InvestigatorOrgMSP.peer',OR('BankAOrgMSP.peer','BankBOrgMSP.peer','SponsorBankOrgMSP.peer'))" \
    ../chaincode/fraudhold/collections_config.json || rc=1
fi
exit $rc
