package main

import (
	"log"

	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

func main() {
	cc, err := contractapi.NewChaincode(&SupplyChainContract{})
	if err != nil {
		log.Panicf("error creating supplychain chaincode: %v", err)
	}
	if err := cc.Start(); err != nil {
		log.Panicf("error starting supplychain chaincode: %v", err)
	}
}
