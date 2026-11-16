#!/usr/bin/env bash
#
# Copyright IBM Corp All Rights Reserved.
#
# SPDX-License-Identifier: Apache-2.0
#

# This is a collection of bash functions used by different scripts

# imports
# test network home var targets to test-network folder
# the reason we use a var here is to accommodate scenarios
# where execution occurs from folders outside of default as $PWD, such as the test-network/addOrg3 folder.
# For setting environment variables, simple relative paths like ".." could lead to unintended references
# due to how they interact with FABRIC_CFG_PATH. It's advised to specify paths more explicitly,
# such as using "../${PWD}", to ensure that Fabric's environment variables are pointing to the correct paths.
TEST_NETWORK_HOME=${TEST_NETWORK_HOME:-${PWD}}
. ${TEST_NETWORK_HOME}/scripts/utils.sh

export CORE_PEER_TLS_ENABLED=true
export ORDERER_CA=${TEST_NETWORK_HOME}/organizations/ordererOrganizations/clearchain.com/tlsca/tlsca.clearchain.com-cert.pem

# Org table. Scripts refer to orgs by number (1-8); index 0 is unused.
# Each org runs a lite peer (peer0, endorses, chaincode is installed here) on
# ORG_LP_PORTS and a committing peer (peer1, joins/approves/commits) ten ports above.
# These must match compose/compose-test-net.yaml.
ORG_COUNT=8
ALL_ORGS="1 2 3 4 5 6 7 8"
ORG_NAMES=("" switch npci banka bankb aggregator sponsorbank buyer investigator)
ORG_MSPS=("" SwitchOrgMSP NPCIOrgMSP BankAOrgMSP BankBOrgMSP AggregatorOrgMSP SponsorBankOrgMSP BuyerOrgMSP InvestigatorOrgMSP)
ORG_LP_PORTS=("" 7051 7151 7251 7351 7451 7551 7651 7751)

# orgDomain ORG -> switch.clearchain.com
orgDomain() {
  echo "${ORG_NAMES[$1]}.clearchain.com"
}

# orgTlsCa ORG -> path to the org's TLS CA certificate
orgTlsCa() {
  local domain
  domain=$(orgDomain $1)
  echo "${TEST_NETWORK_HOME}/organizations/peerOrganizations/${domain}/tlsca/tlsca.${domain}-cert.pem"
}

# orgPeerPort ORG PEER -> host port of peer0 (lite peer) or peer1 (committing peer)
orgPeerPort() {
  echo $((${ORG_LP_PORTS[$1]} + 10 * $2))
}

# Kept for scripts that refer to these by name.
export PEER0_SWITCH_CA=$(orgTlsCa 1)
export PEER0_NPCI_CA=$(orgTlsCa 2)

# Set environment variables for the peer org
# setGlobals ORG [PEER]   ORG is 1-8; PEER is 0 (lite peer) or 1 (committing peer, default)
setGlobals() {

  local USING_ORG=""
  if [ -z "$OVERRIDE_ORG" ]; then
    USING_ORG=$1
    PEER="$2"
    : ${PEER:=1}
  else
    USING_ORG="${OVERRIDE_ORG}"
    : ${PEER:=1}
  fi
  infoln "Using organization ${USING_ORG}"

  if ! [[ "$USING_ORG" =~ ^[1-8]$ ]]; then
    errorln "ORG Unknown: '${USING_ORG}' (expected 1-${ORG_COUNT})"
    return 1
  fi
  if ! [[ "$PEER" =~ ^[01]$ ]]; then
    errorln "PEER Unknown: '${PEER}' (expected 0 for lite peer or 1 for committing peer)"
    return 1
  fi

  local domain
  domain=$(orgDomain $USING_ORG)
  export CORE_PEER_LOCALMSPID=${ORG_MSPS[$USING_ORG]}
  export CORE_PEER_TLS_ROOTCERT_FILE=$(orgTlsCa $USING_ORG)
  export CORE_PEER_MSPCONFIGPATH=${TEST_NETWORK_HOME}/organizations/peerOrganizations/${domain}/users/Admin@${domain}/msp
  export CORE_PEER_ADDRESS=localhost:$(orgPeerPort $USING_ORG $PEER)

  if [ "$VERBOSE" = "true" ]; then
    env | grep CORE
  fi
}

# parsePeerConnectionParameters ORG [ORG...]
# Helper function that sets the peer connection parameters for a chaincode
# operation. Targets each org's committing peer by default (chaincode
# lifecycle); set CONN_PEER=0 to target the lite peers (chaincode invoke).
parsePeerConnectionParameters() {
  PEER_CONN_PARMS=()
  PEERS=""
  local conn_peer=${CONN_PEER:-1}
  while [ "$#" -gt 0 ]; do
    setGlobals $1 $conn_peer || return 1
    PEER="peer${conn_peer}.${ORG_NAMES[$1]}"
    ## Set peer addresses
    if [ -z "$PEERS" ]
    then
	PEERS="$PEER"
    else
	PEERS="$PEERS $PEER"
    fi
    PEER_CONN_PARMS=("${PEER_CONN_PARMS[@]}" --peerAddresses $CORE_PEER_ADDRESS)
    ## Set path to TLS certificate
    PEER_CONN_PARMS=("${PEER_CONN_PARMS[@]}" --tlsRootCertFiles "$(orgTlsCa $1)")
    # shift by one to get to the next organization
    shift
  done
}

verifyResult() {
  if [ $1 -ne 0 ]; then
    fatalln "$2"
  fi
}
