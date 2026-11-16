# details = [
#     {
#         "small": "",
#         "Org": "",
#         "port": "",
#         "ccport": "",
#         "opport": "",
#         "cpport": "",
#         "vsport": ""
#     }
# ]

# lp_template="""
#   #LP(Lite Peer Bank B)
#   peer0.{small}.clearchain.com:
#     <<: *peer-basic-commmon
#     container_name: lp1.{small}
#     environment:
#       - CORE_VM_ENDPOINT=unix:///host/var/run/docker.sock #docker endpoint
#       - CORE_VM_DOCKER_HOSTCONFIG_NETWORKMODE=fabric_test

#       - FABRIC_CFG_PATH=/etc/hyperledger/peercfg
#       - FABRIC_LOGGING_SPEC=INFO
#       #- FABRIC_LOGGING_SPEC=DEBUG
#       - CORE_PEER_TLS_ENABLED=true
#       - CORE_PEER_PROFILE_ENABLED=false
#       - CORE_PEER_TLS_CERT_FILE=/etc/hyperledger/fabric/tls/server.crt
#       - CORE_PEER_TLS_KEY_FILE=/etc/hyperledger/fabric/tls/server.key
#       - CORE_PEER_TLS_ROOTCERT_FILE=/etc/hyperledger/fabric/tls/ca.crt
#       # Peer specific variables
#       - CORE_PEER_ID=peer0.{small}.clearchain.com
#       - CORE_PEER_ADDRESS=peer0.{small}.clearchain.com:{port}
#       - CORE_PEER_LISTENADDRESS=peer0.{small}.clearchain.com:{port}
#       - CORE_PEER_CHAINCODEADDRESS=peer0.{small}.clearchain.com:{ccport}
#       - CORE_PEER_CHAINCODELISTENADDRESS=peer0.{small}.clearchain.com:{ccport}
#       - CORE_PEER_GOSSIP_BOOTSTRAP=peer0.{small}.clearchain.com:{port}
#       - CORE_PEER_GOSSIP_EXTERNALENDPOINT=peer0.{small}.clearchain.com:{port}
#       - CORE_PEER_LOCALMSPID={Org}OrgMSP
#       - CORE_PEER_MSPCONFIGPATH=/etc/hyperledger/fabric/msp
#       - CORE_OPERATIONS_LISTENADDRESS=peer0.{small}.clearchain.com:{opport}
#       - CORE_METRICS_PROVIDER=prometheus
#       - CHAINCODE_AS_A_SERVICE_BUILDER_CONFIG={"peername":"peer0{small}"}
#       - CORE_CHAINCODE_EXECUTETIMEOUT=300s
#       - CORE_PEER_GOSSIP_ORGLEADER=true
#       - CORE_PEER_GOSSIP_STATE_ENABLED=false
#       - CORE_PEER_GOSSIP_USELEADERELECTION=false
#       - CORE_PEER_DELIVERYCLIENT_BLOCKGOSSIPENABLED=false
#       - CORE_PEER_LITEPEER_ENABLED=true
#       #KVSTORE for transient store
#       - CORE_PEER_KVSTORE_ENABLED=true
#       - CORE_PEER_KVSTORE_ADDRESS=hlf_keydb_{small}orgmsp:6379
#       - CORE_PEER_KVSTORE_PASSWORD=
#       - CORE_PEER_KVSTORE_POOLFIFO=true
#       - CORE_PEER_KVSTORE_POOLSIZE=30
#       - CORE_PEER_KVSTORE_MINIDLECONNS=30
#       - CORE_PEER_KVSTORE_MAXACTIVECONNS=30
#       - CORE_PEER_KVSTORE_CONNMAXIDLETIME=10m
#       - CORE_PEER_KVSTORE_DB=0
#       - CORE_PEER_KVSTORE_REPLICAADDRESS=hlf_keydb_{small}orgmsp:6379
#       #StateDatabase
#       - CORE_LEDGER_STATE_STATEDATABASE=sqldb
#       - CORE_LEDGER_STATE_SQLDBCONFIG_ADDRESS=yugabyte.clearchain.com
#       - CORE_LEDGER_STATE_SQLDBCONFIG_PORT=5433
#       - CORE_LEDGER_STATE_SQLDBCONFIG_DBNAME=yugabyte
#       - CORE_LEDGER_STATE_SQLDBCONFIG_USER=yugabyte
#       - CORE_LEDGER_STATE_SQLDBCONFIG_PASSWORD=yugabyte
#       - CORE_LEDGER_STATE_SQLDBCONFIG_SSLMODE=disable
#       - CORE_LEDGER_STATE_SQLDBCONFIG_PREPARESTATEMENT=true
#       - CORE_LEDGER_STATE_SQLDBCONFIG_BATCHSIZE=100
#       - CORE_LEDGER_STATE_SQLDBCONFIG_MAXIDLECONNS=10
#       - CORE_LEDGER_STATE_SQLDBCONFIG_MAXOPENCONNS=10
#       - CORE_LEDGER_STATE_SQLDBCONFIG_CONNMAXLIFETIME=60m
#       #ComittingPeer(CP)
#       - CORE_PEER_COMMITTINGPEER_ID=peer1.{small}.clearchain.com
#       # - CORE_PEER_COMMITTINGPEER_ID=peer4.switch.clearchain.com
#       - CORE_PEER_COMMITTINGPEER_ENDPOINT=dns:///peer1.{small}.clearchain.com:{cpport}
#       # - CORE_PEER_COMMITTINGPEER_ENDPOINT=dns:///peer4.switch.clearchain.com:7081
#       - CORE_PEER_COMMITTINGPEER_TLSCERTPATH=/etc/hyperledger/fabric/cp/tls/ca.crt
#       - CORE_PEER_COMMITTINGPEER_FORWARDCOMMITSTATUSREQUESTS_ENABLED=true

#     volumes:
#       - ../organizations/peerOrganizations/{small}.clearchain.com/peers/peer0.{small}.clearchain.com:/etc/hyperledger/fabric
#       - peer0.{small}.clearchain.com:/var/hyperledger/production
#       - ${DOCKER_SOCK}:/host/var/run/docker.sock # docker sock mounted

#       # - ../organizations/peerOrganizations/switch.clearchain.com/peers/peer4.switch.clearchain.com/tls:/etc/hyperledger/fabric/cp/tls
#       - ../organizations/peerOrganizations/{small}.clearchain.com/peers/peer1.{small}.clearchain.com/tls:/etc/hyperledger/fabric/cp/tls

#     ports:
#       - {ccport}:{ccport}
#       - {opport}:{opport}
# """


# cp_template="""
#   #CP(Committing Peer Bank B)
#   peer1.{small}.clearchain.com:
#     <<: *peer-basic-commmon
#     container_name: cp.{small}
#     environment:
#       - FABRIC_CFG_PATH=/etc/hyperledger/peercfg
#       - FABRIC_LOGGING_SPEC=INFO
#       #- FABRIC_LOGGING_SPEC=INFO
#       - CORE_PEER_TLS_ENABLED=true
#       - CORE_PEER_PROFILE_ENABLED=false
#       - CORE_PEER_TLS_CERT_FILE=/etc/hyperledger/fabric/tls/server.crt
#       - CORE_PEER_TLS_KEY_FILE=/etc/hyperledger/fabric/tls/server.key
#       - CORE_PEER_TLS_ROOTCERT_FILE=/etc/hyperledger/fabric/tls/ca.crt
#       # Peer specific variables
#       - CORE_PEER_ID=peer1.{small}.clearchain.com
#       - CORE_PEER_ADDRESS=peer1.{small}.clearchain.com:{port}
#       - CORE_PEER_LISTENADDRESS=peer1.{small}.clearchain.com:{port}
#       - CORE_PEER_CHAINCODEADDRESS=peer1.{small}.clearchain.com:{ccport}
#       - CORE_PEER_CHAINCODELISTENADDRESS=peer1.{small}.clearchain.com:{ccport}
#       - CORE_PEER_GOSSIP_BOOTSTRAP=peer1.{small}.clearchain.com:{port}
#       - CORE_PEER_GOSSIP_EXTERNALENDPOINT=peer1.{small}.clearchain.com:{port}
#       - CORE_PEER_LOCALMSPID={Org}OrgMSP
#       - CORE_PEER_MSPCONFIGPATH=/etc/hyperledger/fabric/msp
#       - CORE_OPERATIONS_LISTENADDRESS=peer1.{small}.clearchain.com:{opport}
#       - CORE_METRICS_PROVIDER=prometheus
#       - CHAINCODE_AS_A_SERVICE_BUILDER_CONFIG={"peername":"peer1{small}"}
#       - CORE_CHAINCODE_EXECUTETIMEOUT=300s
#       - CORE_PEER_GOSSIP_ORGLEADER=true
#       - CORE_PEER_GOSSIP_STATE_ENABLED=false
#       - CORE_PEER_GOSSIP_USELEADERELECTION=false
#       - CORE_PEER_DELIVERYCLIENT_BLOCKGOSSIPENABLED=false
#       #VSCC(Validation Service)
#       - CORE_PEER_VSCCSERVICE_ENABLED=true
#       - CORE_PEER_VSCCSERVICE_TLS_ENABLED=true
#       - CORE_PEER_VSCCSERVICE_INGRESSENDPOINT=peer1.{small}.clearchain.com
#       - CORE_PEER_VSCCSERVICE_ENDPOINT=peer2.{small}.clearchain.com:{vsport}
#       - CORE_PEER_VSCCSERVICE_BATCHSIZE=10
#       #(Kvstore for transient store)
#       - CORE_PEER_KVSTORE_ENABLED=true
#       - CORE_PEER_KVSTORE_ADDRESS=hlf_keydb_{small}orgmsp:6379
#       - CORE_PEER_KVSTORE_PASSWORD=
#       - CORE_PEER_KVSTORE_POOLFIFO=true
#       - CORE_PEER_KVSTORE_POOLSIZE=30
#       - CORE_PEER_KVSTORE_MINIDLECONNS=30
#       - CORE_PEER_KVSTORE_MAXACTIVECONNS=30
#       - CORE_PEER_KVSTORE_CONNMAXIDLETIME=10m
#       - CORE_PEER_KVSTORE_DB=0
#       - CORE_PEER_KVSTORE_REPLICAADDRESS=hlf_keydb_{small}orgmsp:6379
#       #StateDatabase
#       - CORE_LEDGER_STATE_STATEDATABASE=sqldb
#       - CORE_LEDGER_STATE_SQLDBCONFIG_ADDRESS=yugabyte.clearchain.com
#       - CORE_LEDGER_STATE_SQLDBCONFIG_PORT=5433
#       - CORE_LEDGER_STATE_SQLDBCONFIG_DBNAME=yugabyte
#       - CORE_LEDGER_STATE_SQLDBCONFIG_USER=yugabyte
#       - CORE_LEDGER_STATE_SQLDBCONFIG_PASSWORD=yugabyte
#       - CORE_LEDGER_STATE_SQLDBCONFIG_SSLMODE=disable
#       - CORE_LEDGER_STATE_SQLDBCONFIG_PREPARESTATEMENT=true
#       - CORE_LEDGER_STATE_SQLDBCONFIG_BATCHSIZE=100
#       - CORE_LEDGER_STATE_SQLDBCONFIG_MAXIDLECONNS=10
#       - CORE_LEDGER_STATE_SQLDBCONFIG_MAXOPENCONNS=10
#       - CORE_LEDGER_STATE_SQLDBCONFIG_CONNMAXLIFETIME=60m
      
#     volumes:
#       - ../organizations/peerOrganizations/{small}.clearchain.com/peers/peer1.{small}.clearchain.com:/etc/hyperledger/fabric
#       - peer1.{small}.clearchain.com:/var/hyperledger/production
#     ports:
#       - {port}:{port}
#       - {opport}:{opport}

# """



# vs_template="""
#   #VSCC(Validation Service Bank B)
#   peer2.{small}.clearchain.com:
#     <<: *validation-basic-commmon
#     container_name: vs1.{small}
#     environment:
#       - FABRIC_CFG_PATH=/etc/hyperledger/peercfg
#       - FABRIC_LOGGING_SPEC=INFO
#       #- FABRIC_LOGGING_SPEC=INFO
#       - CORE_PEER_TLS_ENABLED=true
#       - CORE_PEER_PROFILE_ENABLED=false
#       - CORE_PEER_TLS_CERT_FILE=/etc/hyperledger/fabric/tls/server.crt
#       - CORE_PEER_TLS_KEY_FILE=/etc/hyperledger/fabric/tls/server.key
#       - CORE_PEER_TLS_ROOTCERT_FILE=/etc/hyperledger/fabric/tls/ca.crt
#       # Peer specific variables
#       - CORE_PEER_ID=peer1.{small}.clearchain.com
#       # - CORE_PEER_ID=peer4.{small}.clearchain.com
#       - CORE_PEER_ADDRESS=peer2.{small}.clearchain.com:{port}
#       - CORE_PEER_LISTENADDRESS=peer2.{small}.clearchain.com:{port}
  
#       #- CORE_PEER_GOSSIP_EXTERNALENDPOINT=peer2.{small}.clearchain.com:7191
#       - CORE_PEER_LOCALMSPID={Org}OrgMSP
#       - CORE_PEER_MSPCONFIGPATH=/etc/hyperledger/fabric/msp
#       - CORE_METRICS_PROVIDER=prometheus
  
#       #Kvstore for trasient store
#       - CORE_PEER_KVSTORE_ENABLED=true
#       - CORE_PEER_KVSTORE_ADDRESS=hlf_keydb_{small}orgmsp:6379
#       - CORE_PEER_KVSTORE_PASSWORD=
#       - CORE_PEER_KVSTORE_POOLFIFO=true
#       - CORE_PEER_KVSTORE_POOLSIZE=30
#       - CORE_PEER_KVSTORE_MINIDLECONNS=30
#       - CORE_PEER_KVSTORE_MAXACTIVECONNS=30
#       - CORE_PEER_KVSTORE_CONNMAXIDLETIME=10m
#       - CORE_PEER_KVSTORE_DB=0
#       - CORE_PEER_KVSTORE_REPLICAADDRESS=hlf_keydb_{small}orgmsp:6379
#       #StateDatabase
#       - CORE_LEDGER_STATE_STATEDATABASE=sqldb
#       - CORE_LEDGER_STATE_SQLDBCONFIG_ADDRESS=yugabyte.clearchain.com
#       - CORE_LEDGER_STATE_SQLDBCONFIG_PORT=5433
#       - CORE_LEDGER_STATE_SQLDBCONFIG_DBNAME=yugabyte
#       - CORE_LEDGER_STATE_SQLDBCONFIG_USER=yugabyte
#       - CORE_LEDGER_STATE_SQLDBCONFIG_PASSWORD=yugabyte
#       - CORE_LEDGER_STATE_SQLDBCONFIG_SSLMODE=disable
#       - CORE_LEDGER_STATE_SQLDBCONFIG_PREPARESTATEMENT=true
#       - CORE_LEDGER_STATE_SQLDBCONFIG_BATCHSIZE=100
#       - CORE_LEDGER_STATE_SQLDBCONFIG_MAXIDLECONNS=10
#       - CORE_LEDGER_STATE_SQLDBCONFIG_MAXOPENCONNS=10
#       - CORE_LEDGER_STATE_SQLDBCONFIG_CONNMAXLIFETIME=60m
#       #VSCC(Validation Service)
#       - CORE_PEER_VSCCSERVER_CONFIGTXCHANNELNAME="config-tx-channel"
#       - CORE_PEER_VSCCSERVER_CONFIGTXCHANNELTIMEOUT="6s"
#       - CORE_PEER_VALIDATORPOOLSIZE=128
#     volumes:
#       - ../organizations/peerOrganizations/{small}.clearchain.com/peers/peer1.{small}.clearchain.com:/etc/hyperledger/fabric
#       # - ../organizations/peerOrganizations/{small}.clearchain.com/peers/peer4.{small}.clearchain.com:/etc/hyperledger/fabric

#     ports:
#       - {port}:{port}
#       - {opport}:{opport}
  
# """




details = [
    # Aggregator
    {"type": "LP", "small": "aggregator", "Org": "Aggregator", "port": "7451", "ccport": "7452", "opport": "9844",  "cpport": "7461", "vsport": ""},
    {"type": "CP", "small": "aggregator", "Org": "Aggregator", "port": "7461", "ccport": "7462", "opport": "9854",  "cpport": "",     "vsport": "7471"},
    {"type": "VS", "small": "aggregator", "Org": "Aggregator", "port": "7471", "ccport": "",     "opport": "9864",  "cpport": "",     "vsport": ""},

    # SponsorBank
    {"type": "LP", "small": "sponsorbank", "Org": "SponsorBank", "port": "7551", "ccport": "7552", "opport": "9944",  "cpport": "7561", "vsport": ""},
    {"type": "CP", "small": "sponsorbank", "Org": "SponsorBank", "port": "7561", "ccport": "7562", "opport": "9954",  "cpport": "",     "vsport": "7571"},
    {"type": "VS", "small": "sponsorbank", "Org": "SponsorBank", "port": "7571", "ccport": "",     "opport": "9964",  "cpport": "",     "vsport": ""},

    # Buyer
    {"type": "LP", "small": "buyer", "Org": "Buyer", "port": "7651", "ccport": "7652", "opport": "10044", "cpport": "7661", "vsport": ""},
    {"type": "CP", "small": "buyer", "Org": "Buyer", "port": "7661", "ccport": "7662", "opport": "10054", "cpport": "",     "vsport": "7671"},
    {"type": "VS", "small": "buyer", "Org": "Buyer", "port": "7671", "ccport": "",     "opport": "10064", "cpport": "",     "vsport": ""},

    # Investigator
    {"type": "LP", "small": "investigator", "Org": "Investigator", "port": "7751", "ccport": "7752", "opport": "10144", "cpport": "7761", "vsport": ""},
    {"type": "CP", "small": "investigator", "Org": "Investigator", "port": "7761", "ccport": "7762", "opport": "10154", "cpport": "",     "vsport": "7771"},
    {"type": "VS", "small": "investigator", "Org": "Investigator", "port": "7771", "ccport": "",     "opport": "10164", "cpport": "",     "vsport": ""},
]

lp_template="""
  #LP(Lite Peer)
  peer0.{small}.clearchain.com:
    <<: *peer-basic-commmon
    container_name: lp1.{small}
    environment:
      - CORE_VM_ENDPOINT=unix:///host/var/run/docker.sock #docker endpoint
      - CORE_VM_DOCKER_HOSTCONFIG_NETWORKMODE=fabric_test

      - FABRIC_CFG_PATH=/etc/hyperledger/peercfg
      - FABRIC_LOGGING_SPEC=INFO
      #- FABRIC_LOGGING_SPEC=DEBUG
      - CORE_PEER_TLS_ENABLED=true
      - CORE_PEER_PROFILE_ENABLED=false
      - CORE_PEER_TLS_CERT_FILE=/etc/hyperledger/fabric/tls/server.crt
      - CORE_PEER_TLS_KEY_FILE=/etc/hyperledger/fabric/tls/server.key
      - CORE_PEER_TLS_ROOTCERT_FILE=/etc/hyperledger/fabric/tls/ca.crt
      # Peer specific variables
      - CORE_PEER_ID=peer0.{small}.clearchain.com
      - CORE_PEER_ADDRESS=peer0.{small}.clearchain.com:{port}
      - CORE_PEER_LISTENADDRESS=peer0.{small}.clearchain.com:{port}
      - CORE_PEER_CHAINCODEADDRESS=peer0.{small}.clearchain.com:{ccport}
      - CORE_PEER_CHAINCODELISTENADDRESS=peer0.{small}.clearchain.com:{ccport}
      - CORE_PEER_GOSSIP_BOOTSTRAP=peer0.{small}.clearchain.com:{port}
      - CORE_PEER_GOSSIP_EXTERNALENDPOINT=peer0.{small}.clearchain.com:{port}
      - CORE_PEER_LOCALMSPID={Org}OrgMSP
      - CORE_PEER_MSPCONFIGPATH=/etc/hyperledger/fabric/msp
      - CORE_OPERATIONS_LISTENADDRESS=peer0.{small}.clearchain.com:{opport}
      - CORE_METRICS_PROVIDER=prometheus
      - CHAINCODE_AS_A_SERVICE_BUILDER_CONFIG={"peername":"peer0{small}"}
      - CORE_CHAINCODE_EXECUTETIMEOUT=300s
      - CORE_PEER_GOSSIP_ORGLEADER=true
      - CORE_PEER_GOSSIP_STATE_ENABLED=false
      - CORE_PEER_GOSSIP_USELEADERELECTION=false
      - CORE_PEER_DELIVERYCLIENT_BLOCKGOSSIPENABLED=false
      - CORE_PEER_LITEPEER_ENABLED=true
      #KVSTORE for transient store
      - CORE_PEER_KVSTORE_ENABLED=true
      - CORE_PEER_KVSTORE_ADDRESS=hlf_keydb_{small}orgmsp:6379
      - CORE_PEER_KVSTORE_PASSWORD=
      - CORE_PEER_KVSTORE_POOLFIFO=true
      - CORE_PEER_KVSTORE_POOLSIZE=30
      - CORE_PEER_KVSTORE_MINIDLECONNS=30
      - CORE_PEER_KVSTORE_MAXACTIVECONNS=30
      - CORE_PEER_KVSTORE_CONNMAXIDLETIME=10m
      - CORE_PEER_KVSTORE_DB=0
      - CORE_PEER_KVSTORE_REPLICAADDRESS=hlf_keydb_{small}orgmsp:6379
      #StateDatabase
      - CORE_LEDGER_STATE_STATEDATABASE=sqldb
      - CORE_LEDGER_STATE_SQLDBCONFIG_ADDRESS=yugabyte.clearchain.com
      - CORE_LEDGER_STATE_SQLDBCONFIG_PORT=5433
      - CORE_LEDGER_STATE_SQLDBCONFIG_DBNAME=yugabyte
      - CORE_LEDGER_STATE_SQLDBCONFIG_USER=yugabyte
      - CORE_LEDGER_STATE_SQLDBCONFIG_PASSWORD=yugabyte
      - CORE_LEDGER_STATE_SQLDBCONFIG_SSLMODE=disable
      - CORE_LEDGER_STATE_SQLDBCONFIG_PREPARESTATEMENT=true
      - CORE_LEDGER_STATE_SQLDBCONFIG_BATCHSIZE=100
      - CORE_LEDGER_STATE_SQLDBCONFIG_MAXIDLECONNS=10
      - CORE_LEDGER_STATE_SQLDBCONFIG_MAXOPENCONNS=10
      - CORE_LEDGER_STATE_SQLDBCONFIG_CONNMAXLIFETIME=60m
      #ComittingPeer(CP)
      - CORE_PEER_COMMITTINGPEER_ID=peer1.{small}.clearchain.com
      # - CORE_PEER_COMMITTINGPEER_ID=peer4.switch.clearchain.com
      - CORE_PEER_COMMITTINGPEER_ENDPOINT=dns:///peer1.{small}.clearchain.com:{cpport}
      # - CORE_PEER_COMMITTINGPEER_ENDPOINT=dns:///peer4.switch.clearchain.com:7081
      - CORE_PEER_COMMITTINGPEER_TLSCERTPATH=/etc/hyperledger/fabric/cp/tls/ca.crt
      - CORE_PEER_COMMITTINGPEER_FORWARDCOMMITSTATUSREQUESTS_ENABLED=true

    volumes:
      - ../organizations/peerOrganizations/{small}.clearchain.com/peers/peer0.{small}.clearchain.com:/etc/hyperledger/fabric
      - peer0.{small}.clearchain.com:/var/hyperledger/production
      - ${DOCKER_SOCK}:/host/var/run/docker.sock # docker sock mounted

      # - ../organizations/peerOrganizations/switch.clearchain.com/peers/peer4.switch.clearchain.com/tls:/etc/hyperledger/fabric/cp/tls
      - ../organizations/peerOrganizations/{small}.clearchain.com/peers/peer1.{small}.clearchain.com/tls:/etc/hyperledger/fabric/cp/tls

    ports:
      - {port}:{port}
      - {opport}:{opport}
"""


cp_template="""
  #CP(Committing Peer)
  peer1.{small}.clearchain.com:
    <<: *peer-basic-commmon
    container_name: cp.{small}
    environment:
      - FABRIC_CFG_PATH=/etc/hyperledger/peercfg
      - FABRIC_LOGGING_SPEC=INFO
      #- FABRIC_LOGGING_SPEC=INFO
      - CORE_PEER_TLS_ENABLED=true
      - CORE_PEER_PROFILE_ENABLED=false
      - CORE_PEER_TLS_CERT_FILE=/etc/hyperledger/fabric/tls/server.crt
      - CORE_PEER_TLS_KEY_FILE=/etc/hyperledger/fabric/tls/server.key
      - CORE_PEER_TLS_ROOTCERT_FILE=/etc/hyperledger/fabric/tls/ca.crt
      # Peer specific variables
      - CORE_PEER_ID=peer1.{small}.clearchain.com
      - CORE_PEER_ADDRESS=peer1.{small}.clearchain.com:{port}
      - CORE_PEER_LISTENADDRESS=peer1.{small}.clearchain.com:{port}
      - CORE_PEER_CHAINCODEADDRESS=peer1.{small}.clearchain.com:{ccport}
      - CORE_PEER_CHAINCODELISTENADDRESS=peer1.{small}.clearchain.com:{ccport}
      - CORE_PEER_GOSSIP_BOOTSTRAP=peer1.{small}.clearchain.com:{port}
      - CORE_PEER_GOSSIP_EXTERNALENDPOINT=peer1.{small}.clearchain.com:{port}
      - CORE_PEER_LOCALMSPID={Org}OrgMSP
      - CORE_PEER_MSPCONFIGPATH=/etc/hyperledger/fabric/msp
      - CORE_OPERATIONS_LISTENADDRESS=peer1.{small}.clearchain.com:{opport}
      - CORE_METRICS_PROVIDER=prometheus
      - CHAINCODE_AS_A_SERVICE_BUILDER_CONFIG={"peername":"peer1{small}"}
      - CORE_CHAINCODE_EXECUTETIMEOUT=300s
      - CORE_PEER_GOSSIP_ORGLEADER=true
      - CORE_PEER_GOSSIP_STATE_ENABLED=false
      - CORE_PEER_GOSSIP_USELEADERELECTION=false
      - CORE_PEER_DELIVERYCLIENT_BLOCKGOSSIPENABLED=false
      #VSCC(Validation Service)
      - CORE_PEER_VSCCSERVICE_ENABLED=true
      - CORE_PEER_VSCCSERVICE_TLS_ENABLED=true
      - CORE_PEER_VSCCSERVICE_INGRESSENDPOINT=peer1.{small}.clearchain.com
      - CORE_PEER_VSCCSERVICE_ENDPOINT=peer2.{small}.clearchain.com:{vsport}
      - CORE_PEER_VSCCSERVICE_BATCHSIZE=10
      #(Kvstore for transient store)
      - CORE_PEER_KVSTORE_ENABLED=true
      - CORE_PEER_KVSTORE_ADDRESS=hlf_keydb_{small}orgmsp:6379
      - CORE_PEER_KVSTORE_PASSWORD=
      - CORE_PEER_KVSTORE_POOLFIFO=true
      - CORE_PEER_KVSTORE_POOLSIZE=30
      - CORE_PEER_KVSTORE_MINIDLECONNS=30
      - CORE_PEER_KVSTORE_MAXACTIVECONNS=30
      - CORE_PEER_KVSTORE_CONNMAXIDLETIME=10m
      - CORE_PEER_KVSTORE_DB=0
      - CORE_PEER_KVSTORE_REPLICAADDRESS=hlf_keydb_{small}orgmsp:6379
      #StateDatabase
      - CORE_LEDGER_STATE_STATEDATABASE=sqldb
      - CORE_LEDGER_STATE_SQLDBCONFIG_ADDRESS=yugabyte.clearchain.com
      - CORE_LEDGER_STATE_SQLDBCONFIG_PORT=5433
      - CORE_LEDGER_STATE_SQLDBCONFIG_DBNAME=yugabyte
      - CORE_LEDGER_STATE_SQLDBCONFIG_USER=yugabyte
      - CORE_LEDGER_STATE_SQLDBCONFIG_PASSWORD=yugabyte
      - CORE_LEDGER_STATE_SQLDBCONFIG_SSLMODE=disable
      - CORE_LEDGER_STATE_SQLDBCONFIG_PREPARESTATEMENT=true
      - CORE_LEDGER_STATE_SQLDBCONFIG_BATCHSIZE=100
      - CORE_LEDGER_STATE_SQLDBCONFIG_MAXIDLECONNS=10
      - CORE_LEDGER_STATE_SQLDBCONFIG_MAXOPENCONNS=10
      - CORE_LEDGER_STATE_SQLDBCONFIG_CONNMAXLIFETIME=60m
      
    volumes:
      - ../organizations/peerOrganizations/{small}.clearchain.com/peers/peer1.{small}.clearchain.com:/etc/hyperledger/fabric
      - peer1.{small}.clearchain.com:/var/hyperledger/production
    ports:
      - {port}:{port}
      - {opport}:{opport}

"""



vs_template="""
  #VSCC(Validation Service)
  peer2.{small}.clearchain.com:
    <<: *validation-basic-commmon
    container_name: vs1.{small}
    environment:
      - FABRIC_CFG_PATH=/etc/hyperledger/peercfg
      - FABRIC_LOGGING_SPEC=INFO
      #- FABRIC_LOGGING_SPEC=INFO
      - CORE_PEER_TLS_ENABLED=true
      - CORE_PEER_PROFILE_ENABLED=false
      - CORE_PEER_TLS_CERT_FILE=/etc/hyperledger/fabric/tls/server.crt
      - CORE_PEER_TLS_KEY_FILE=/etc/hyperledger/fabric/tls/server.key
      - CORE_PEER_TLS_ROOTCERT_FILE=/etc/hyperledger/fabric/tls/ca.crt
      # Peer specific variables
      - CORE_PEER_ID=peer1.{small}.clearchain.com
      # - CORE_PEER_ID=peer4.{small}.clearchain.com
      - CORE_PEER_ADDRESS=peer2.{small}.clearchain.com:{port}
      - CORE_PEER_LISTENADDRESS=peer2.{small}.clearchain.com:{port}
  
      #- CORE_PEER_GOSSIP_EXTERNALENDPOINT=peer2.{small}.clearchain.com:7191
      - CORE_PEER_LOCALMSPID={Org}OrgMSP
      - CORE_PEER_MSPCONFIGPATH=/etc/hyperledger/fabric/msp
      - CORE_METRICS_PROVIDER=prometheus
  
      #Kvstore for trasient store
      - CORE_PEER_KVSTORE_ENABLED=true
      - CORE_PEER_KVSTORE_ADDRESS=hlf_keydb_{small}orgmsp:6379
      - CORE_PEER_KVSTORE_PASSWORD=
      - CORE_PEER_KVSTORE_POOLFIFO=true
      - CORE_PEER_KVSTORE_POOLSIZE=30
      - CORE_PEER_KVSTORE_MINIDLECONNS=30
      - CORE_PEER_KVSTORE_MAXACTIVECONNS=30
      - CORE_PEER_KVSTORE_CONNMAXIDLETIME=10m
      - CORE_PEER_KVSTORE_DB=0
      - CORE_PEER_KVSTORE_REPLICAADDRESS=hlf_keydb_{small}orgmsp:6379
      #StateDatabase
      - CORE_LEDGER_STATE_STATEDATABASE=sqldb
      - CORE_LEDGER_STATE_SQLDBCONFIG_ADDRESS=yugabyte.clearchain.com
      - CORE_LEDGER_STATE_SQLDBCONFIG_PORT=5433
      - CORE_LEDGER_STATE_SQLDBCONFIG_DBNAME=yugabyte
      - CORE_LEDGER_STATE_SQLDBCONFIG_USER=yugabyte
      - CORE_LEDGER_STATE_SQLDBCONFIG_PASSWORD=yugabyte
      - CORE_LEDGER_STATE_SQLDBCONFIG_SSLMODE=disable
      - CORE_LEDGER_STATE_SQLDBCONFIG_PREPARESTATEMENT=true
      - CORE_LEDGER_STATE_SQLDBCONFIG_BATCHSIZE=100
      - CORE_LEDGER_STATE_SQLDBCONFIG_MAXIDLECONNS=10
      - CORE_LEDGER_STATE_SQLDBCONFIG_MAXOPENCONNS=10
      - CORE_LEDGER_STATE_SQLDBCONFIG_CONNMAXLIFETIME=60m
      #VSCC(Validation Service)
      - CORE_PEER_VSCCSERVER_CONFIGTXCHANNELNAME="config-tx-channel"
      - CORE_PEER_VSCCSERVER_CONFIGTXCHANNELTIMEOUT="6s"
      - CORE_PEER_VALIDATORPOOLSIZE=128
    volumes:
      - ../organizations/peerOrganizations/{small}.clearchain.com/peers/peer1.{small}.clearchain.com:/etc/hyperledger/fabric
      # - ../organizations/peerOrganizations/{small}.clearchain.com/peers/peer4.{small}.clearchain.com:/etc/hyperledger/fabric

    ports:
      - {port}:{port}
      - {opport}:{opport}
  
"""


def render(template, d):
    # plain replace, because .format() breaks on the JSON braces and ${DOCKER_SOCK}
    for k, v in d.items():
        template = template.replace("{" + k + "}", str(v))
    return template


templates = {"LP": lp_template, "CP": cp_template, "VS": vs_template}
files = {"LP": "lp.txt", "CP": "cp.txt", "VS": "vs.txt"}

with open(files["LP"], "w") as lp, open(files["CP"], "w") as cp, open(files["VS"], "w") as vs:
    out = {"LP": lp, "CP": cp, "VS": vs}
    for d in details:
        out[d["type"]].write(render(templates[d["type"]], d))