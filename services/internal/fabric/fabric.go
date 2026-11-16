// Package fabric connects ClearChain services to the Drunix network through an
// org's lite peer, using the Fabric Gateway client API.
package fabric

import (
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hyperledger/fabric-gateway/pkg/client"
	"github.com/hyperledger/fabric-gateway/pkg/hash"
	"github.com/hyperledger/fabric-gateway/pkg/identity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Org describes one network member. LPPort is its lite peer's port on the host;
// these match clearchain-test-network/compose/compose-test-net.yaml.
type Org struct {
	Name   string
	MSP    string
	LPPort int
}

var orgs = map[string]Org{
	"switch":       {"switch", "SwitchOrgMSP", 7051},
	"npci":         {"npci", "NPCIOrgMSP", 7151},
	"banka":        {"banka", "BankAOrgMSP", 7251},
	"bankb":        {"bankb", "BankBOrgMSP", 7351},
	"aggregator":   {"aggregator", "AggregatorOrgMSP", 7451},
	"sponsorbank":  {"sponsorbank", "SponsorBankOrgMSP", 7551},
	"buyer":        {"buyer", "BuyerOrgMSP", 7651},
	"investigator": {"investigator", "InvestigatorOrgMSP", 7751},
}

// LookupOrg returns the org with the given short name, e.g. "switch".
func LookupOrg(name string) (Org, error) {
	o, ok := orgs[name]
	if !ok {
		return Org{}, fmt.Errorf("unknown org %q", name)
	}
	return o, nil
}

// Client is a gateway connection for one identity on one channel.
type Client struct {
	Org     Org
	conn    *grpc.ClientConn
	Gateway *client.Gateway
	Network *client.Network
}

// Close releases the gateway and its gRPC connection.
func (c *Client) Close() {
	c.Gateway.Close()
	c.conn.Close()
}

// Contract returns a handle on a chaincode deployed to the client's channel.
func (c *Client) Contract(chaincode string) *client.Contract {
	return c.Network.GetContract(chaincode)
}

// orgsDir is where the network's generated certificates live. Override with
// CLEARCHAIN_ORGS_DIR when not running from the services folder.
func orgsDir() string {
	if d := os.Getenv("CLEARCHAIN_ORGS_DIR"); d != "" {
		return d
	}
	return filepath.Join("..", "clearchain-test-network", "organizations")
}

// Connect opens a gateway connection to orgName's lite peer as the given user
// (for example "User1" or "Admin") on the given channel. Use it to submit and
// evaluate transactions.
func Connect(orgName, user, channel string) (*Client, error) {
	return connect(orgName, user, channel, 0)
}

// ConnectCommitter opens a gateway connection to orgName's committing peer.
// Lite peers keep no ledger, so block and chaincode events are served only by
// the committing peer; use this connection to subscribe to them.
func ConnectCommitter(orgName, user, channel string) (*Client, error) {
	return connect(orgName, user, channel, 1)
}

// connect dials peer0 (lite peer) or peer1 (committing peer, ten ports above).
func connect(orgName, user, channel string, peer int) (*Client, error) {
	org, err := LookupOrg(orgName)
	if err != nil {
		return nil, err
	}
	domain := org.Name + ".clearchain.com"
	orgPath := filepath.Join(orgsDir(), "peerOrganizations", domain)
	userMSP := filepath.Join(orgPath, "users", user+"@"+domain, "msp")
	peerHost := fmt.Sprintf("peer%d.%s", peer, domain)

	tlsPEM, err := os.ReadFile(filepath.Join(orgPath, "peers", peerHost, "tls", "ca.crt"))
	if err != nil {
		return nil, fmt.Errorf("read peer TLS certificate: %w", err)
	}
	tlsCert, err := identity.CertificateFromPEM(tlsPEM)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(tlsCert)
	conn, err := grpc.NewClient(
		fmt.Sprintf("dns:///localhost:%d", org.LPPort+10*peer),
		grpc.WithTransportCredentials(credentials.NewClientTLSFromCert(pool, peerHost)),
	)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", peerHost, err)
	}

	certPEM, err := readOnlyFile(filepath.Join(userMSP, "signcerts"))
	if err != nil {
		conn.Close()
		return nil, err
	}
	cert, err := identity.CertificateFromPEM(certPEM)
	if err != nil {
		conn.Close()
		return nil, err
	}
	id, err := identity.NewX509Identity(org.MSP, cert)
	if err != nil {
		conn.Close()
		return nil, err
	}
	keyPEM, err := readOnlyFile(filepath.Join(userMSP, "keystore"))
	if err != nil {
		conn.Close()
		return nil, err
	}
	key, err := identity.PrivateKeyFromPEM(keyPEM)
	if err != nil {
		conn.Close()
		return nil, err
	}
	sign, err := identity.NewPrivateKeySign(key)
	if err != nil {
		conn.Close()
		return nil, err
	}

	gw, err := client.Connect(
		id,
		client.WithSign(sign),
		client.WithHash(hash.SHA256),
		client.WithClientConnection(conn),
		client.WithEvaluateTimeout(5*time.Second),
		client.WithEndorseTimeout(15*time.Second),
		client.WithSubmitTimeout(5*time.Second),
		client.WithCommitStatusTimeout(30*time.Second),
	)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("connect gateway as %s@%s: %w", user, domain, err)
	}
	return &Client{Org: org, conn: conn, Gateway: gw, Network: gw.GetNetwork(channel)}, nil
}

// readOnlyFile reads the single file expected in dir (a cert or key folder).
func readOnlyFile(dir string) ([]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			return os.ReadFile(filepath.Join(dir, e.Name()))
		}
	}
	return nil, fmt.Errorf("no file in %s", dir)
}
