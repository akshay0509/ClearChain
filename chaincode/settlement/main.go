package main

import (
	"log"

	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

func main() {
	cc, err := contractapi.NewChaincode(&SettlementContract{})
	if err != nil {
		log.Panicf("error creating settlement chaincode: %v", err)
	}
	if err := cc.Start(); err != nil {
		log.Panicf("error starting settlement chaincode: %v", err)
	}
}
