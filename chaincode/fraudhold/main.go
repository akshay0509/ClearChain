package main

import (
	"log"

	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

func main() {
	cc, err := contractapi.NewChaincode(&FraudHoldContract{})
	if err != nil {
		log.Panicf("error creating fraudhold chaincode: %v", err)
	}
	if err := cc.Start(); err != nil {
		log.Panicf("error starting fraudhold chaincode: %v", err)
	}
}
