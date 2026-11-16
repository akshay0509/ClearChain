package main

import (
	"log"

	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

func main() {
	cc, err := contractapi.NewChaincode(&HealthContract{})
	if err != nil {
		log.Panicf("error creating healthflag chaincode: %v", err)
	}
	if err := cc.Start(); err != nil {
		log.Panicf("error starting healthflag chaincode: %v", err)
	}
}
