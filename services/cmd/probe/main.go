// Command probe checks the two things the payment switch depends on:
// transactions that need endorsement from two orgs go through the gateway, and
// chaincode commit events reach a subscribed client.
//
// Run from the services folder with the network up and healthflag deployed.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"clearchain/services/internal/fabric"

	"github.com/hyperledger/fabric-gateway/pkg/client"
	"google.golang.org/grpc/status"
)

const (
	channel   = "mychannel"
	chaincode = "healthflag"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "FAILED:", describe(err))
		os.Exit(1)
	}
}

func run() error {
	npci, err := fabric.Connect("npci", "User1", channel)
	if err != nil {
		return err
	}
	defer npci.Close()
	sw, err := fabric.Connect("switch", "User1", channel)
	if err != nil {
		return err
	}
	defer sw.Close()
	bankB, err := fabric.Connect("bankb", "User1", channel)
	if err != nil {
		return err
	}
	defer bankB.Close()

	// 1. Subscribe first, as the switch would.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	swEvents, err := fabric.ConnectCommitter("switch", "User1", channel)
	if err != nil {
		return err
	}
	defer swEvents.Close()
	events, err := swEvents.Network.ChaincodeEvents(ctx, chaincode)
	if err != nil {
		return fmt.Errorf("subscribe to chaincode events: %w", err)
	}
	fmt.Println("subscribed to", chaincode, "events through the switch committing peer")

	// 2. The operator registers two banks. The chaincode policy needs both the
	//    switch's and the operator's peers to endorse, so this exercises
	//    cross-org endorsement through the gateway.
	for _, b := range [][3]string{{"BANKA", "Bank A", "BankAOrgMSP"}, {"BANKB", "Bank B", "BankBOrgMSP"}} {
		start := time.Now()
		_, err := npci.Contract(chaincode).SubmitTransaction("RegisterBank", b[0], b[1], b[2])
		switch {
		case err == nil:
			fmt.Printf("registered %s in %v\n", b[0], time.Since(start).Round(time.Millisecond))
		case strings.Contains(describe(err), "already registered"):
			fmt.Printf("%s was already registered\n", b[0])
		default:
			return fmt.Errorf("register %s: %w", b[0], err)
		}
	}

	// 3. Read back as the switch.
	flags, err := sw.Contract(chaincode).EvaluateTransaction("GetAllFlags")
	if err != nil {
		return fmt.Errorf("read flags: %w", err)
	}
	fmt.Printf("flags as seen by the switch: %s\n", flags)

	// 4. Bank B changes its own degraded-mode behaviour; time how long the
	//    resulting event takes to reach the subscriber after the commit.
	mode, hold := "HOLD_RETRY", "2000"
	if strings.Contains(string(flags), `"bankId":"BANKB"`) && strings.Contains(string(flags), `"mode":"HOLD_RETRY"`) {
		mode, hold = "FAIL_FAST", "0"
	}
	submitted := time.Now()
	if _, err := bankB.Contract(chaincode).SubmitTransaction("SetBankMode", "BANKB", mode, hold); err != nil {
		return fmt.Errorf("set bank mode: %w", err)
	}
	committed := time.Now()
	fmt.Printf("Bank B set mode %s; submit-to-commit took %v\n", mode, committed.Sub(submitted).Round(time.Millisecond))

	deadline := time.After(20 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return errors.New("event stream closed before the expected event arrived")
			}
			fmt.Printf("event %s block=%d tx=%.12s\n", ev.EventName, ev.BlockNumber, ev.TransactionID)
			if strings.Contains(string(ev.Payload), `"mode":"`+mode+`"`) && strings.Contains(string(ev.Payload), `"bankId":"BANKB"`) {
				fmt.Printf("OK: mode-change event received %v after commit status returned\n", time.Since(committed).Round(time.Millisecond))
				return nil
			}
		case <-deadline:
			return errors.New("no event within 20s of the commit")
		}
	}
}

// describe unwraps gateway errors so the peer's own message is visible.
func describe(err error) string {
	msg := err.Error()
	var commitErr *client.CommitError
	if errors.As(err, &commitErr) {
		msg += fmt.Sprintf(" [commit status %s]", commitErr.Code)
	}
	if st, ok := status.FromError(err); ok {
		for _, d := range st.Details() {
			msg += fmt.Sprintf(" | %v", d)
		}
	}
	return msg
}
