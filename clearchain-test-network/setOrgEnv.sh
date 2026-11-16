#!/usr/bin/env bash
#
# SPDX-License-Identifier: Apache-2.0




# default to using Switch
ORG=${1:-Switch}

# Exit on first error, print all commands.
set -e
set -o pipefail

# Where am I?
DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )/.." && pwd )"

ORDERER_CA=${DIR}/test-network/organizations/ordererOrganizations/clearchain.com/tlsca/tlsca.clearchain.com-cert.pem
PEER0_SWITCH_CA=${DIR}/test-network/organizations/peerOrganizations/switch.clearchain.com/tlsca/tlsca.switch.clearchain.com-cert.pem
PEER0_NPCI_CA=${DIR}/test-network/organizations/peerOrganizations/npci.clearchain.com/tlsca/tlsca.npci.clearchain.com-cert.pem

PEER0_BANKA_CA=${DIR}/test-network/organizations/peerOrganizations/banka.clearchain.com/tlsca/tlsca.banka.clearchain.com-cert.pem

PEER0_BANKB_CA=${DIR}/test-network/organizations/peerOrganizations/bankb.clearchain.com/tlsca/tlsca.bankb.clearchain.com-cert.pem

PEER0_AGGREGATOR_CA=${DIR}/test-network/organizations/peerOrganizations/aggregator.clearchain.com/tlsca/tlsca.aggregator.clearchain.com-cert.pem

PEER0_SPONSORBANK_CA=${DIR}/test-network/organizations/peerOrganizations/sponsorbank.clearchain.com/tlsca/tlsca.sponsorbank.clearchain.com-cert.pem

PEER0_BUYER_CA=${DIR}/test-network/organizations/peerOrganizations/buyer.clearchain.com/tlsca/tlsca.buyer.clearchain.com-cert.pem

PEER0_INVESTIGATOR_CA=${DIR}/test-network/organizations/peerOrganizations/investigator.clearchain.com/tlsca/tlsca.investigator.clearchain.com-cert.pem



if [[ ${ORG,,} == "switch" || ${ORG,,} == "digibank" ]]; then

   CORE_PEER_LOCALMSPID=SwitchOrgMSP
   CORE_PEER_MSPCONFIGPATH=${DIR}/test-network/organizations/peerOrganizations/switch.clearchain.com/users/Admin@switch.clearchain.com/msp
   CORE_PEER_ADDRESS=localhost:7051
   CORE_PEER_TLS_ROOTCERT_FILE=${DIR}/test-network/organizations/peerOrganizations/switch.clearchain.com/tlsca/tlsca.switch.clearchain.com-cert.pem

elif [[ ${ORG,,} == "npci" || ${ORG,,} == "magnetocorp" ]]; then

   CORE_PEER_LOCALMSPID=NPCIOrgMSP
   CORE_PEER_MSPCONFIGPATH=${DIR}/test-network/organizations/peerOrganizations/npci.clearchain.com/users/Admin@npci.clearchain.com/msp
   CORE_PEER_ADDRESS=localhost:7151
   CORE_PEER_TLS_ROOTCERT_FILE=${DIR}/test-network/organizations/peerOrganizations/npci.clearchain.com/tlsca/tlsca.npci.clearchain.com-cert.pem

elif [[ ${ORG,,} == "banka" ]]; then

   CORE_PEER_LOCALMSPID=BankAOrgMSP
   CORE_PEER_MSPCONFIGPATH=${DIR}/test-network/organizations/peerOrganizations/banka.clearchain.com/users/Admin@banka.clearchain.com/msp
   CORE_PEER_ADDRESS=localhost:7251
   CORE_PEER_TLS_ROOTCERT_FILE=${PEER0_BANKA_CA}

elif [[ ${ORG,,} == "bankb" ]]; then

   CORE_PEER_LOCALMSPID=BankBOrgMSP
   CORE_PEER_MSPCONFIGPATH=${DIR}/test-network/organizations/peerOrganizations/bankb.clearchain.com/users/Admin@bankb.clearchain.com/msp
   CORE_PEER_ADDRESS=localhost:7351
   CORE_PEER_TLS_ROOTCERT_FILE=${PEER0_BANKB_CA}

elif [[ ${ORG,,} == "aggregator" ]]; then

   CORE_PEER_LOCALMSPID=AggregatorOrgMSP
   CORE_PEER_MSPCONFIGPATH=${DIR}/test-network/organizations/peerOrganizations/aggregator.clearchain.com/users/Admin@aggregator.clearchain.com/msp
   CORE_PEER_ADDRESS=localhost:7451
   CORE_PEER_TLS_ROOTCERT_FILE=${PEER0_AGGREGATOR_CA}

elif [[ ${ORG,,} == "sponsorbank" ]]; then

   CORE_PEER_LOCALMSPID=SponsorBankOrgMSP
   CORE_PEER_MSPCONFIGPATH=${DIR}/test-network/organizations/peerOrganizations/sponsorbank.clearchain.com/users/Admin@sponsorbank.clearchain.com/msp
   CORE_PEER_ADDRESS=localhost:7551
   CORE_PEER_TLS_ROOTCERT_FILE=${PEER0_SPONSORBANK_CA}

elif [[ ${ORG,,} == "buyer" ]]; then

   CORE_PEER_LOCALMSPID=BuyerOrgMSP
   CORE_PEER_MSPCONFIGPATH=${DIR}/test-network/organizations/peerOrganizations/buyer.clearchain.com/users/Admin@buyer.clearchain.com/msp
   CORE_PEER_ADDRESS=localhost:7651
   CORE_PEER_TLS_ROOTCERT_FILE=${PEER0_BUYER_CA}

elif [[ ${ORG,,} == "investigator" ]]; then

   CORE_PEER_LOCALMSPID=InvestigatorOrgMSP
   CORE_PEER_MSPCONFIGPATH=${DIR}/test-network/organizations/peerOrganizations/investigator.clearchain.com/users/Admin@investigator.clearchain.com/msp
   CORE_PEER_ADDRESS=localhost:7751
   CORE_PEER_TLS_ROOTCERT_FILE=${PEER0_INVESTIGATOR_CA}

else
   echo "Unknown \"$ORG\", please choose Switch/Digibank or NPCI/Magnetocorp"
   echo "For example to get the environment variables to set upa NPCI shell environment run:  ./setOrgEnv.sh NPCI"
   echo
   echo "This can be automated to set them as well with:"
   echo
   echo 'export $(./setOrgEnv.sh NPCI | xargs)'
   exit 1
fi

# output the variables that need to be set
echo "CORE_PEER_TLS_ENABLED=true"
echo "ORDERER_CA=${ORDERER_CA}"
echo "PEER0_SWITCH_CA=${PEER0_SWITCH_CA}"
echo "PEER0_NPCI_CA=${PEER0_NPCI_CA}"
echo "PEER0_BANKA_CA=${PEER0_BANKA_CA}"
echo "PEER0_BANKB_CA=${PEER0_BANKB_CA}"
echo "PEER0_AGGREGATOR_CA=${PEER0_AGGREGATOR_CA}"
echo "PEER0_SPONSORBANK_CA=${PEER0_SPONSORBANK_CA}"
echo "PEER0_BUYER_CA=${PEER0_BUYER_CA}"
echo "PEER0_INVESTIGATOR_CA=${PEER0_INVESTIGATOR_CA}"

echo "CORE_PEER_MSPCONFIGPATH=${CORE_PEER_MSPCONFIGPATH}"
echo "CORE_PEER_ADDRESS=${CORE_PEER_ADDRESS}"
echo "CORE_PEER_TLS_ROOTCERT_FILE=${CORE_PEER_TLS_ROOTCERT_FILE}"

echo "CORE_PEER_LOCALMSPID=${CORE_PEER_LOCALMSPID}"
