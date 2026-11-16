#!/usr/bin/env bash

function one_line_pem {
    echo "`awk 'NF {sub(/\\n/, ""); printf "%s\\\\\\\n",$0;}' $1`"
}

function json_ccp {
    local PP=$(one_line_pem $4)
    local CP=$(one_line_pem $5)
    sed -e "s/\${ORG}/$1/" \
        -e "s/\${P0PORT}/$2/" \
        -e "s/\${CAPORT}/$3/" \
        -e "s#\${PEERPEM}#$PP#" \
        -e "s#\${CAPEM}#$CP#" \
        organizations/ccp-template.json
}

function yaml_ccp {
    local PP=$(one_line_pem $4)
    local CP=$(one_line_pem $5)
    sed -e "s/\${ORG}/$1/" \
        -e "s/\${P0PORT}/$2/" \
        -e "s/\${CAPORT}/$3/" \
        -e "s#\${PEERPEM}#$PP#" \
        -e "s#\${CAPEM}#$CP#" \
        organizations/ccp-template.yaml | sed -e $'s/\\\\n/\\\n          /g'
}

ORG=1
P0PORT=7051
CAPORT=7054
PEERPEM=organizations/peerOrganizations/switch.clearchain.com/tlsca/tlsca.switch.clearchain.com-cert.pem
CAPEM=organizations/peerOrganizations/switch.clearchain.com/ca/ca.switch.clearchain.com-cert.pem

echo "$(json_ccp $ORG $P0PORT $CAPORT $PEERPEM $CAPEM)" > organizations/peerOrganizations/switch.clearchain.com/connection-switch.json
echo "$(yaml_ccp $ORG $P0PORT $CAPORT $PEERPEM $CAPEM)" > organizations/peerOrganizations/switch.clearchain.com/connection-switch.yaml

ORG=2
P0PORT=7151
CAPORT=7154
PEERPEM=organizations/peerOrganizations/npci.clearchain.com/tlsca/tlsca.npci.clearchain.com-cert.pem
CAPEM=organizations/peerOrganizations/npci.clearchain.com/ca/ca.npci.clearchain.com-cert.pem

echo "$(json_ccp $ORG $P0PORT $CAPORT $PEERPEM $CAPEM)" > organizations/peerOrganizations/npci.clearchain.com/connection-npci.json
echo "$(yaml_ccp $ORG $P0PORT $CAPORT $PEERPEM $CAPEM)" > organizations/peerOrganizations/npci.clearchain.com/connection-npci.yaml

ORG=3
P0PORT=7251
CAPORT=7254
PEERPEM=organizations/peerOrganizations/banka.clearchain.com/tlsca/tlsca.banka.clearchain.com-cert.pem
CAPEM=organizations/peerOrganizations/banka.clearchain.com/ca/ca.banka.clearchain.com-cert.pem

echo "$(json_ccp $ORG $P0PORT $CAPORT $PEERPEM $CAPEM)" > organizations/peerOrganizations/banka.clearchain.com/connection-banka.json
echo "$(yaml_ccp $ORG $P0PORT $CAPORT $PEERPEM $CAPEM)" > organizations/peerOrganizations/banka.clearchain.com/connection-banka.yaml


ORG=4
P0PORT=7351
CAPORT=7354
PEERPEM=organizations/peerOrganizations/bankb.clearchain.com/tlsca/tlsca.bankb.clearchain.com-cert.pem
CAPEM=organizations/peerOrganizations/bankb.clearchain.com/ca/ca.bankb.clearchain.com-cert.pem

echo "$(json_ccp $ORG $P0PORT $CAPORT $PEERPEM $CAPEM)" > organizations/peerOrganizations/bankb.clearchain.com/connection-bankb.json
echo "$(yaml_ccp $ORG $P0PORT $CAPORT $PEERPEM $CAPEM)" > organizations/peerOrganizations/bankb.clearchain.com/connection-bankb.yaml


ORG=5
P0PORT=7451
CAPORT=7454
PEERPEM=organizations/peerOrganizations/aggregator.clearchain.com/tlsca/tlsca.aggregator.clearchain.com-cert.pem
CAPEM=organizations/peerOrganizations/aggregator.clearchain.com/ca/ca.aggregator.clearchain.com-cert.pem

echo "$(json_ccp $ORG $P0PORT $CAPORT $PEERPEM $CAPEM)" > organizations/peerOrganizations/aggregator.clearchain.com/connection-aggregator.json
echo "$(yaml_ccp $ORG $P0PORT $CAPORT $PEERPEM $CAPEM)" > organizations/peerOrganizations/aggregator.clearchain.com/connection-aggregator.yaml



ORG=6
P0PORT=7551
CAPORT=7554
PEERPEM=organizations/peerOrganizations/sponsorbank.clearchain.com/tlsca/tlsca.sponsorbank.clearchain.com-cert.pem
CAPEM=organizations/peerOrganizations/sponsorbank.clearchain.com/ca/ca.sponsorbank.clearchain.com-cert.pem

echo "$(json_ccp $ORG $P0PORT $CAPORT $PEERPEM $CAPEM)" > organizations/peerOrganizations/sponsorbank.clearchain.com/connection-sponsorbank.json
echo "$(yaml_ccp $ORG $P0PORT $CAPORT $PEERPEM $CAPEM)" > organizations/peerOrganizations/sponsorbank.clearchain.com/connection-sponsorbank.yaml


ORG=7
P0PORT=7651
CAPORT=7654
PEERPEM=organizations/peerOrganizations/buyer.clearchain.com/tlsca/tlsca.buyer.clearchain.com-cert.pem
CAPEM=organizations/peerOrganizations/buyer.clearchain.com/ca/ca.buyer.clearchain.com-cert.pem

echo "$(json_ccp $ORG $P0PORT $CAPORT $PEERPEM $CAPEM)" > organizations/peerOrganizations/buyer.clearchain.com/connection-buyer.json
echo "$(yaml_ccp $ORG $P0PORT $CAPORT $PEERPEM $CAPEM)" > organizations/peerOrganizations/buyer.clearchain.com/connection-buyer.yaml



ORG=8
P0PORT=7751
CAPORT=7754
PEERPEM=organizations/peerOrganizations/investigator.clearchain.com/tlsca/tlsca.investigator.clearchain.com-cert.pem
CAPEM=organizations/peerOrganizations/investigator.clearchain.com/ca/ca.investigator.clearchain.com-cert.pem

echo "$(json_ccp $ORG $P0PORT $CAPORT $PEERPEM $CAPEM)" > organizations/peerOrganizations/investigator.clearchain.com/connection-investigator.json
echo "$(yaml_ccp $ORG $P0PORT $CAPORT $PEERPEM $CAPEM)" > organizations/peerOrganizations/investigator.clearchain.com/connection-investigator.yaml