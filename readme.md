# ClearChain

A shared coordination and settlement layer for banks, payment switches, aggregators and businesses, built on [Drunix](https://github.com/npci/drunix) (NPCI's fork of Hyperledger Fabric).

ClearChain has four parts. This README covers what exists today, how to run it, and what is left to build.

| Pillar | What it does | Status |
|---|---|---|
| 1. Health-aware, fail-fast switch | Switch and NPCI jointly flag a degraded bank; payments to it are declined before any debit | **Working** |
| 2. Instant merchant settlement | Float payout on confirmed escrow credit, duplicate rejection by RRN, auto-reversal of stuck payments | Not built |
| 3. Multi-tier supplier payouts | Pre-registered obligations paid to every tier in one commit | Not built |
| 4. Targeted fraud holds | Hold only the disputed amount, jointly endorsed by bank and investigator | Not built |

## Repository layout

```
clearchain-test-network/   8-org Drunix network: compose files, channel config, scripts
chaincode/healthflag/      Pillar 1 smart contract (Go) and its unit tests
services/
  cmd/switch/              Payment switch: local flag cache, fail-fast routing, flag proposals
  cmd/operator/            NPCI hop: sits between switch and banks, measures banks independently
  cmd/banksim/             Stand-in for bank core systems; can be made to degrade
  cmd/loadgen/             Sends a steady stream of payments
  cmd/probe/               Registers the demo banks and checks events reach a client
  internal/                Shared code: gateway connection, flag cache, health measurement
  demo-pillar1.sh          Scripted pillar 1 demo
tools/wsl-sync.sh          Copies a Windows working copy into WSL with Linux line endings
```

## The network

Eight organisations on one channel (`mychannel`) with a single Raft orderer:

| # | Org | MSP ID | Lite peer port | Committing peer port | Database port |
|---|---|---|---|---|---|
| 1 | Switch | SwitchOrgMSP | 7051 | 7061 | 5433 |
| 2 | NPCI | NPCIOrgMSP | 7151 | 7161 | 5434 |
| 3 | Bank A | BankAOrgMSP | 7251 | 7261 | 5435 |
| 4 | Bank B | BankBOrgMSP | 7351 | 7361 | 5436 |
| 5 | Aggregator | AggregatorOrgMSP | 7451 | 7461 | 5437 |
| 6 | Sponsor Bank | SponsorBankOrgMSP | 7551 | 7561 | 5438 |
| 7 | Buyer | BuyerOrgMSP | 7651 | 7661 | 5439 |
| 8 | Investigator | InvestigatorOrgMSP | 7751 | 7761 | 5440 |

Each org runs a lite peer (endorses, runs chaincode), a committing peer (validates and commits), a validation service, its own YugabyteDB and its own KeyDB. That is 41 containers, plus one per org for each deployed chaincode, and about 7 GB of memory.

Three things about Drunix that the setup depends on:

- **Every org needs its own YugabyteDB.** Drunix creates a table per chaincode. If two orgs share a database their committing peers collide creating it and crash.
- **Commit events come from the committing peer, not the lite peer.** Lite peers keep no ledger. Clients submit through the lite peer and subscribe to events through the committing peer.
- **Deploying a chaincode takes about 4 minutes**, because each of the eight lite peers builds it.

## One-time setup

The network runs on Linux. On Windows use WSL2 with Ubuntu and Docker Desktop (WSL integration enabled for Ubuntu). Give WSL at least 12 GB in `C:\Users\<you>\.wslconfig`:

```
[wsl2]
memory=12GB
```

Everything below runs inside Ubuntu, in the Linux filesystem (not under `/mnt/c` or `/mnt/d`).

**1. Tools.** Install build tools and Go 1.26.1 or newer:

```bash
sudo apt update && sudo apt install -y git make build-essential jq bc curl
```

```bash
curl -fsSLO https://go.dev/dl/go1.26.1.linux-amd64.tar.gz && sudo tar -C /usr/local -xzf go1.26.1.linux-amd64.tar.gz
```

**2. Drunix.** Clone it and build the command-line binaries (takes a few minutes):

```bash
git clone https://github.com/npci/drunix.git ~/drunix
```

```bash
cd ~/drunix && PATH=$PATH:/usr/local/go/bin make peer orderer configtxgen configtxlator osnadmin cryptogen discover ledgerutil
```

**3. Chaincode build images.** The network script does not pull these:

```bash
docker pull npcioss/drunix-ccenv:1.0 && docker pull npcioss/drunix-baseos:1.0
```

**4. ClearChain.** Clone this repository to `~/ClearChain`, then link Drunix's config folder into it (the scripts expect `../config`):

```bash
ln -s ~/drunix/drunix-network/config ~/ClearChain/config
```

**5. Path.** Run this in every new terminal before using the network scripts:

```bash
export PATH=$HOME/drunix/build/bin:$PATH:/usr/local/go/bin
```

## Running pillar 1

**1. Start the network and create the channel** (about 2 minutes):

```bash
cd ~/ClearChain/clearchain-test-network && ./network.sh up createChannel
```

**2. Deploy the health-flag contract.** The `-ccep` flag sets its endorsement policy: every transaction needs both the Switch's and NPCI's peers to sign.

```bash
./network.sh deployCC -ccn healthflag -ccp ../chaincode/healthflag -ccl go -ccep "AND('SwitchOrgMSP.peer','NPCIOrgMSP.peer')"
```

**3. Build the services:**

```bash
cd ~/ClearChain/services && mkdir -p bin && go build -o bin/ ./cmd/...
```

**4. Register the demo banks.** This also confirms cross-org endorsement and event delivery work. It should end with `OK: mode-change event received ...`.

```bash
./bin/probe
```

**5. Run the demo** (about 2.5 minutes):

```bash
./demo-pillar1.sh
```

**6. Stop the network when finished.** This deletes the ledger and all generated certificates.

```bash
cd ~/ClearChain/clearchain-test-network && ./network.sh down
```

### What the demo does

1. Starts the bank simulator, the NPCI hop and two switches. `switch-1` measures banks and proposes flags; `switch-2` only listens.
2. Sends 20 payments a second from Bank A customers to Bank B merchants.
3. Makes Bank B's core system degrade (slow responses, 30% of requests never answered).
4. Waits for the flag. The switch and NPCI each record what they measured; the switch proposes `DEGRADED`; the contract accepts only because NPCI's own measurement agrees.
5. Shows how long each switch's cache took to pick up the committed flag.
6. Sends one payment through each switch. Both decline it with `PAYEE_BANK_DEGRADED`, and the payer bank's own record shows it was never debited.
7. Restores Bank B. After a 30-second cool-down the flag moves to `PROBING` and 1 in 20 payments is let through. Once enough of those succeed for both observers, the flag returns to `HEALTHY`.
8. Prints payment outcomes, routing decision times and what each bank actually did.

### Results from a reference run

| Demo target | Result |
|---|---|
| Flag needs switch and operator to agree | Committed 8.9 s after the bank began to struggle |
| Cache updates within about 1 s of commit | Both switches updated in the same millisecond as the commit |
| Next payment fails fast, before any debit | Declined in under 1 ms on both switches; no debit at the payer bank |
| Routing decision under 10 ms | Median 2.5 microseconds, worst 0.13 ms over 2,601 payments |

In the seconds before the flag landed, 68 payments were debited without confirmation. After it, none were. Those stuck payments are what pillar 2's watcher will auto-reverse.

### Running the pieces by hand

Each in its own terminal, from `~/ClearChain/services`:

```bash
./bin/banksim
```

```bash
./bin/operator
```

```bash
./bin/switch -name switch-1 -addr :9100
```

Then, from another terminal:

| Action | Command |
|---|---|
| Send one payment | `curl -X POST localhost:9100/pay -d '{"rrn":"R1","payerBank":"BANKA","payeeBank":"BANKB","amount":25000}'` |
| Degrade a bank | `curl -X POST localhost:9101/admin/bank/BANKB/mode -d '{"mode":"degraded"}'` (also `healthy`, `down`) |
| Switch state (flags, outcomes, timings) | `curl localhost:9100/state` |
| What a bank did for one payment | `curl localhost:9101/bank/BANKA/payment/R1` |
| Generate load | `./bin/loadgen -rate 20 -duration 60s` |

Amounts are in paise. Logs from the demo script are in `services/logs/`.

### Unit tests

```bash
cd ~/ClearChain/chaincode/healthflag && go test ./...
```

```bash
cd ~/ClearChain/services && go test ./...
```

## How pillar 1 works

- **Two independent measurements.** The switch and NPCI each measure a bank from the payments they actually send it, and each writes its own observation to the ledger (`SubmitObservation`).
- **Proposal with a built-in check.** Only a switch can call `ProposeFlag`. The contract rejects it unless NPCI's recent observation also shows the bank degraded. The endorsement policy additionally requires NPCI's peer to sign.
- **The bank has no say over its status.** A bank cannot submit observations or propose flags. It can only choose what switches do while it is degraded (`SetBankMode`): fail the payment at once, or hold and retry for a set time.
- **Routing never waits on the ledger.** The switch reads a local in-memory cache. Commit events keep the cache current.
- **No rerouting.** The payee's bank is fixed, so a payment to a degraded bank is declined before the payer is debited, or held and retried against the same bank.
- **Guarded recovery.** `DEGRADED` to `PROBING` only after the cool-down; `PROBING` to `HEALTHY` only when probe payments succeed for both observers.

Thresholds (sample size, timeout and failure rates, latency, cool-down, probe share) live on the ledger and only NPCI can change them (`SetConfig`).

## What is left to build

**Pillar 1, to finish**

- Exercise hold-and-retry mode in a live run (implemented, unit-tested in the contract, not yet demonstrated).
- Move health measurements into a Private Data Collection; today they are public on the ledger.

**Pillar 2: settlement**

- Settlement contract: record each payment keyed by RRN, reject duplicates, float payout on sponsor-bank confirmation with merchant / fee / GST split, float reimbursement.
- Endorsement policy: Aggregator and Sponsor Bank.
- Watcher service: find payments nearing the deadline and request auto-reversal; the contract checks the on-chain timestamp itself.

**Pillar 3: supply chain**

- Obligations contract: each tier registers what it owes the tier below (amount, due date, invoice, Udyam number); the anchor buyer's payment splits across all registered tiers in one commit.
- Suppliers are registered identities under existing orgs, not separate orgs. A registration must be signed by the tier that owes the money.
- Section 43B(h) deadline view.

**Pillar 4: fraud holds**

- Holds contract: reject a hold without a legal provision and case reference; hold only the disputed amount; pro-rata split across victims; detect duplicate holds on the same funds.
- Endorsement policy: the holding bank and the Investigator.

**Across all pillars**

- Dashboard with three views (bank health and routing, supply-chain flow, fraud tracing and holds), reading YugabyteDB.
- Load test: 50 to 100 concurrent payments with no duplicate processing, and throughput measured as lite peers and validation services are added.
- Confirm how Drunix enables mutual TLS and Sparse Block Delivery, both named in the proposal.

## Known gaps in the network scripts

- `scripts/deployCCAAS.sh` (chaincode-as-a-service) still handles only Switch and NPCI. Use `deployCC`.
- `crypto-config/` under `clearchain-test-network` holds unused sample certificates and keys from upstream and can be deleted.
- Database and KeyDB credentials are development defaults.

## Working from Windows

If you edit on Windows and run in WSL, `tools/wsl-sync.sh` copies the working copy at `/mnt/d/GIT/ClearChain` into `~/ClearChain`, converting line endings. Change the `SRC` path at the top if your checkout lives elsewhere.
