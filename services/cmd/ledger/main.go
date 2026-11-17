// Command ledger reads from, or submits to, a chaincode as a chosen org.
//
//	ledger sponsorbank settlement GetFloat
//	ledger switch settlement GetPayment RRN000123
//	ledger -submit npci settlement RequestReversal RRN000123
//
// It prints the result, or the reason the peers refused.
package main

import (
	"flag"
	"fmt"
	"os"

	"clearchain/services/internal/fabric"
)

func main() {
	submit := flag.Bool("submit", false, "submit a transaction instead of evaluating a query")
	user := flag.String("user", "User1", "identity within the org")
	channel := flag.String("channel", "mychannel", "channel name")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: ledger [-submit] [-user User1] <org> <chaincode> <function> [args...]")
	}
	flag.Parse()
	args := flag.Args()
	if len(args) < 3 {
		flag.Usage()
		os.Exit(2)
	}
	cl, err := fabric.Connect(args[0], *user, *channel)
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAILED:", err)
		os.Exit(1)
	}
	defer cl.Close()
	contract := cl.Contract(args[1])
	var out []byte
	if *submit {
		out, err = contract.SubmitTransaction(args[2], args[3:]...)
	} else {
		out, err = contract.EvaluateTransaction(args[2], args[3:]...)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "REFUSED:", fabric.Describe(err))
		os.Exit(1)
	}
	fmt.Println(string(out))
}
