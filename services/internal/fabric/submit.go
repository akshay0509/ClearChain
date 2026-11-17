package fabric

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/hyperledger/fabric-gateway/pkg/client"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"google.golang.org/grpc/status"
)

// Describe returns an error's message together with the detail the peers sent
// back, which the gateway error alone does not show.
func Describe(err error) string {
	if err == nil {
		return ""
	}
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

// Mentions reports whether the error, including peer detail, contains text.
func Mentions(err error, text string) bool {
	return err != nil && strings.Contains(Describe(err), text)
}

// Conflict reports whether a transaction was endorsed and ordered but then
// invalidated because another transaction in the same block changed a key it
// had read. Nothing was written, so it is safe to submit again.
func Conflict(err error) bool {
	var commitErr *client.CommitError
	if !errors.As(err, &commitErr) {
		return false
	}
	return commitErr.Code == peer.TxValidationCode_MVCC_READ_CONFLICT ||
		commitErr.Code == peer.TxValidationCode_PHANTOM_READ_CONFLICT
}

// EndorsersDisagreed reports whether a transaction was dropped before ordering
// because the endorsing peers simulated it against slightly different state:
// one had already applied a block the other had not. Nothing was submitted, so
// it is safe to try again.
func EndorsersDisagreed(err error) bool {
	return Mentions(err, "ProposalResponsePayloads do not match")
}

// SubmitRetry submits a transaction, trying again when it loses a race with
// another transaction for the same key, either at endorsement or at commit.
// It returns the result, how many retries were needed, and the error.
func SubmitRetry(contract *client.Contract, attempts int, fn string, args ...string) (result []byte, retries int, err error) {
	for {
		result, err = contract.SubmitTransaction(fn, args...)
		if err == nil || !(Conflict(err) || EndorsersDisagreed(err)) || retries >= attempts-1 {
			return result, retries, err
		}
		retries++
		time.Sleep(time.Duration(20+rand.Intn(120)) * time.Millisecond)
	}
}

// Events subscribes to a chaincode's events through org's committing peer.
// With replay set, delivery starts from the first block, so a service that
// restarts sees everything it missed. The returned function closes the
// connection.
func Events(ctx context.Context, org, channel, chaincode string, replay bool) (<-chan *client.ChaincodeEvent, func(), error) {
	cl, err := ConnectCommitter(org, "User1", channel)
	if err != nil {
		return nil, nil, err
	}
	var opts []client.ChaincodeEventsOption
	if replay {
		opts = append(opts, client.WithStartBlock(0))
	}
	events, err := cl.Network.ChaincodeEvents(ctx, chaincode, opts...)
	if err != nil {
		cl.Close()
		return nil, nil, fmt.Errorf("subscribe to %s events: %w", chaincode, err)
	}
	return events, cl.Close, nil
}
