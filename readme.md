# ClearChain

A shared coordination and settlement layer for banks, payment switches, aggregators and businesses, built on [Drunix](https://github.com/npci/drunix) (NPCI's fork of Hyperledger Fabric).

ClearChain has four parts. All four run on a live 8-org Drunix network, each with its own smart contract, endorsement policy and scripted demo.

| Pillar | What it does | Endorsed by |
|---|---|---|
| 1. Health-aware, fail-fast switch | Switch and NPCI jointly flag a degraded bank; payments to it are declined before any debit | Switch AND NPCI |
| 2. Instant merchant settlement | Float payout once the escrow credit is confirmed; duplicate rejection by RRN; auto-reversal of stuck payments | Aggregator AND Sponsor Bank |
| 3. Multi-tier supplier payouts | Each tier registers what it owes the next; the buyer's one payment credits every tier in one commit | Buyer AND Bank A AND Bank B |
| 4. Targeted fraud holds | Trace fraud money across banks; hold only the disputed amount; pro-rata recovery | Investigator AND the holding bank |

## Repository layout

```
clearchain-test-network/     8-org Drunix network: compose files, channel config, scripts
chaincode/
  healthflag/                Pillar 1: bank health flags
  settlement/                Pillar 2: payments by RRN, float payouts, reversals
  supplychain/               Pillar 3: multi-tier obligations (+ private collection config)
  fraudhold/                 Pillar 4: complaints, tracing, holds, recovery (+ private collections)
services/
  cmd/setup/                 One-time registration of banks, merchants, deadline rule and float
  cmd/switch/                Payment switch: flag cache, fail-fast routing, records payments on-chain
  cmd/operator/              NPCI hop: measures banks independently, keeps a settlement file
  cmd/banksim/               Stand-in for bank core systems: payments, accounts, liens, outages
  cmd/bankadapter/           Banks' link to the ledger: confirms credits, pays merchants, runs the cycle
  cmd/aggregator/            Aggregator's settlement service: requests float payouts
  cmd/watcher/               Turnaround-time watcher: requests auto-reversal before the deadline
  cmd/scdemo/                Pillar 3 driver: each supplier acts with its own identity
  cmd/fraudemo/              Pillar 4 driver: banks and the Investigator act with their own identities
  cmd/ledger/                Query or submit to any contract as any org
  cmd/loadgen/               Payment load, steady or in a burst
  cmd/probe/                 Checks cross-org endorsement and event delivery
  internal/                  Gateway connection, retries, flag cache, health measurement
  demo-pillar{1,2,3,4}.sh    Scripted demos
  recon.sh                   SQL reconciliation of the operator's settlement file against the ledger
tools/
  deploy-contracts.sh        Deploys all four contracts with their policies
  wsl-sync.sh                Copies a Windows working copy into WSL with Linux line endings
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

Each org runs a lite peer (endorses, runs chaincode), a committing peer (validates and commits), a validation service, its own YugabyteDB and its own KeyDB: 41 containers, plus one per installed chaincode. The whole network uses about 7.5 GB of memory.

Each contract is installed only on the orgs that endorse or read it:

| Contract | Installed on | Private collections |
|---|---|---|
| healthflag | Switch, NPCI | none |
| settlement | Switch, NPCI, Aggregator, Sponsor Bank | none (account details are stored only as hashes) |
| supplychain | Bank A, Bank B, Buyer | `scTerms`: amounts and invoice references, shared by Buyer, Bank A, Bank B |
| fraudhold | Bank A, Bank B, Sponsor Bank, Investigator | `caseBankA`, `caseBankB`, `caseSponsorBank`: each shared only by that bank and the Investigator |

Bank A and Bank B also issue certificates to the supply-chain suppliers (`User2` and `User3` under each bank), so suppliers sign their own transactions without running nodes.

### Drunix behaviours the setup depends on

- **Every org needs its own YugabyteDB.** Drunix creates a table per chaincode in each org's database. If two orgs share a database their committing peers collide creating it and crash.
- **Commit events come from the committing peer.** Lite peers keep no ledger. Clients submit through the lite peer and subscribe to events through the committing peer.
- **The block cut time is `ORDERER_GENERAL_DRUNIX_BLOCKCUTTIME`.** The upstream compose sets `ORDERER_GENERAL_BLOCKCUTTIME`, which Drunix does not read. ClearChain sets 0.5 s, which brings a commit from about 2.1 s down to about 0.6 s.
- **The orderer's data must be on its volume.** The upstream compose put the orderer's ledger and Raft log inside the container, so recreating it lost the channel. ClearChain's compose keeps them on the volume.
- **A contract's first use on an org can race.** The lite peer and committing peer can both try to create the contract's table; the clients retry that error.

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

**4. ClearChain.** Clone this repository to `~/ClearChain`, link Drunix's config folder into it (the scripts expect `../config`), and make the scripts executable:

```bash
ln -s ~/drunix/drunix-network/config ~/ClearChain/config
```

```bash
cd ~/ClearChain && git ls-files '*.sh' | xargs chmod +x
```

**5. Path.** Run this in every new terminal before using the network scripts:

```bash
export PATH=$HOME/drunix/build/bin:$PATH:/usr/local/go/bin
```

## Running the demos

**1. Start the network and create the channel** (about 2 minutes):

```bash
cd ~/ClearChain/clearchain-test-network && ./network.sh up createChannel
```

**2. Deploy the four contracts** (about 6 minutes):

```bash
~/ClearChain/tools/deploy-contracts.sh
```

**3. Build the services:**

```bash
cd ~/ClearChain/services && mkdir -p bin && go build -o bin/ ./cmd/...
```

**4. Run the demos**, in any order, from `~/ClearChain/services`. Each registers what it needs and is safe to run again. Each prints every step and exits non-zero if any expectation fails.

```bash
./demo-pillar1.sh
```

```bash
./demo-pillar2.sh
```

```bash
./demo-pillar3.sh
```

```bash
./demo-pillar4.sh
```

**5. Stop the network when finished.** This deletes the ledger and all generated certificates.

```bash
cd ~/ClearChain/clearchain-test-network && ./network.sh down
```

### Unit tests

```bash
for m in chaincode/healthflag chaincode/settlement chaincode/supplychain chaincode/fraudhold services; do (cd ~/ClearChain/$m && go test ./...); done
```

## Pillar 1: health-aware, fail-fast switch

### What the demo does

1. Starts the bank simulator, the NPCI hop and two switches. `switch-1` measures banks and proposes flags; `switch-2` only listens.
2. Sends 20 payments a second from Bank A customers to Bank B merchants.
3. Makes Bank B's core system degrade (slow responses, 30% of requests never answered).
4. Waits for the flag. The switch and NPCI each record what they measured; the switch proposes `DEGRADED`; the contract accepts only because NPCI's own measurement agrees.
5. Shows how long each switch's cache took to pick up the committed flag.
6. Sends one payment through each switch. Both decline it with `PAYEE_BANK_DEGRADED`, and the payer bank's own record shows it was never debited.
7. Restores Bank B. After a 30-second cool-down the flag moves to `PROBING` and 1 in 20 payments is let through. Once enough of those succeed for both observers, the flag returns to `HEALTHY`.

### Results from a reference run

| Demo target | Result |
|---|---|
| Flag needs switch and operator to agree | Committed 6.1 s after the bank began to struggle |
| Cache updates within about 1 s of commit | Both switches updated in the same millisecond as the commit |
| Next payment fails fast, before any debit | Declined in under 1 ms on both switches; no debit at the payer bank |
| Routing decision under 10 ms | Median 4.9 microseconds, worst 0.07 ms over 2,601 payments |

### How it works

- **Two independent measurements.** The switch and NPCI each measure a bank from the payments they send it, and each writes its own observation to the ledger.
- **Proposal with a built-in check.** Only a switch can propose a flag change. The contract rejects it unless NPCI's recent observation also shows the bank degraded, and the endorsement policy requires NPCI's peer to sign.
- **The bank has no say over its status.** It can only choose what switches do while it is degraded: fail the payment at once, or hold and retry for a set time.
- **Routing never waits on the ledger.** The switch reads a local in-memory cache that commit events keep current.
- **No rerouting.** The payee's bank is fixed, so a payment to a degraded bank is declined before the payer is debited.
- **Guarded recovery.** `DEGRADED` to `PROBING` only after the cool-down; `PROBING` to `HEALTHY` only when probe payments succeed for both observers.

Thresholds live on the ledger and only NPCI can change them.

## Pillar 2: instant merchant settlement

### What the demo does

1. Registers the banks, two merchants (2% fee, 18% GST on the fee), a 60-second demo deadline (the real rule is T+1) and a Rs 50 lakh float.
2. A customer pays Raju Electronics Rs 1,000. The switch records the payment on the ledger keyed by its RRN; the sponsor bank confirms the escrow credit; the aggregator requests a payout; the contract checks the float, deducts the merchant's share and records the split; the sponsor bank pays the merchant.
3. A payment is debited but the sponsor bank never confirms it. A payout for it is refused, and so is an early reversal request.
4. The same RRN arrives again, at the same switch and at a different one. The second switch's record is refused by the contract as a duplicate, and so is a second payout.
5. 100 merchant payments arrive at the same instant.
6. The stuck payment reaches its reversal window; the watcher asks; the contract checks its own timestamps and reverses it; the payer bank returns the money.
7. The aggregator's settlement cycle repays the float from escrow.
8. Reconciles NPCI's settlement file against the ledger with SQL (`recon.sh`).

### Results from a reference run

| Demo target | Result |
|---|---|
| Merchant paid within a few seconds of the escrow confirmation | 2.3 s from the escrow credit landing to the merchant being paid |
| Payout only after a confirmed escrow credit | Refused while unconfirmed: "a payout needs the escrow credit to be confirmed first" |
| Stuck payment auto-reversed before the deadline, no float advanced | Reversed 46 s after the debit, 14 s before the deadline; float untouched |
| 50-100 concurrent payments, no duplicate processing | 100 at once: 100 payouts for 100 distinct RRNs; float fell by exactly their total |
| Reconciliation is a SQL query | 101 of 101 settlement-file lines matched the ledger; 0 mismatches |

Under the 100-payment burst, payouts took a median of about 4-8 s from confirmation, because each of a payment's three ledger steps waits for a block and payouts compete for float lanes (about 40 resubmissions per burst).

### How it works

- **One record per RRN.** A second record for the same RRN is refused by the contract, independent of the ledger's own conflict checks.
- **Payout tied to confirmation.** Only the payee bank's org can confirm a credit, and only the aggregator can request a payout, which the contract allows only for a confirmed payment.
- **Float lanes.** The float is split into 32 balances so concurrent payouts rarely touch the same key.
- **The watcher only asks.** The contract decides from the payment's own on-chain timestamps. The deadline runs from the earlier of the debit and the record, so a delayed record never gains time.
- **No personal data on the ledger.** Payer and payee are stored only as SHA-256 hashes.

## Pillar 3: multi-tier supplier payouts

### What the demo does

Anchor Motors (buyer) owes Precision Castings (Tier-1, medium) Rs 10 lakh. Tier-1 owes Shree Forgings (Tier-2, small) Rs 6 lakh. Tier-2 owes Raju Tooling (Tier-3, micro) Rs 2.5 lakh.

1. Each party registers itself with its own identity.
2. The buyer opens the chain. Each supplier registers what it owes the next tier; the tier above approves.
3. Refused along the way: a supplier opening a chain, a tier registering before it has a place, the buyer registering on a supplier's behalf, passing down more than is owed, the beneficiary approving its own obligation, and approval by anyone but the tier above.
4. Shows the Section 43B(h) view today and four days later.
5. Shows that the Switch org cannot read amounts or invoice references.
6. The buyer releases one payment. One transaction credits Tier-1 Rs 4 lakh, Tier-2 Rs 3.5 lakh and Tier-3 Rs 2.5 lakh; the buyer's bank sees the event and credits each supplier's bank.
7. A second release is refused.

### Results from a reference run

| Demo target | Result |
|---|---|
| All pre-registered tiers credited in the same commit | 3 tiers credited by one transaction; the split adds up to the buyer's Rs 10 lakh |
| Nobody commits another tier's money | 6 kinds of unauthorised action refused by the contract |
| Live 43B(h) deadline view | Tier-3 flagged DUE_SOON with 2 days left; OVERDUE four days later; medium enterprise marked not covered |
| Contract values private | Switch org's database: 0 rows mentioning an amount or invoice reference |

### How it works

- **Suppliers are identities, not nodes.** Each supplier signs with its own certificate, issued under its bank. The contract binds each party to exactly one certificate.
- **Consent at every tier.** Only the debtor can register what it owes, and only the tier above can approve it. Unapproved obligations are left out of the release.
- **43B(h).** The statutory date follows MSMED Act Section 15: the agreed period, capped at 45 days, or 15 days with no written agreement; micro and small enterprises only.

## Pillar 4: targeted fraud holds

### What the demo does

Victim 1 (Bank A) pays a mule account at Bank B Rs 60,000; victim 2 (Sponsor Bank) pays it Rs 40,000. The mule moves Rs 30,000 on to a second account at Bank A, and also holds Rs 25,000 of its own money.

1. Each victim's bank files a complaint. The victim's details go only to that bank and the Investigator.
2. The receiving banks record where the money went, and the trace shows how much can still be held at each stop.
3. Refused holds: no legal provision; no case reference; holding all Rs 60,000 when half has moved on; endorsement by the wrong bank; the Investigator acting alone.
4. Three holds on exactly the disputed amounts. Bank B marks a Rs 70,000 lien and the account holder can still spend the other Rs 25,000.
5. A second complaint about the same money is flagged as overlapping, and its hold is refused as a duplicate.
6. Bank B, which holds the mule account, is refused access to victim 1's details.
7. A court order recovers Rs 50,000 from the mule account, split Rs 30,000 and Rs 20,000 by what each victim lost; each victim's bank confirms repayment. A second order recovers the Rs 30,000 that moved on.

### Results from a reference run

| Demo target | Result |
|---|---|
| Hold missing a legal provision or case reference rejected | Both refused by the contract |
| Hold only the disputed amount | Rs 70,000 held of a Rs 95,000 balance; Rs 25,000 stayed usable |
| Duplicate holds on the same funds detected | Second complaint flagged as overlapping; its hold refused as a duplicate |
| Pro-rata split across victims | Rs 50,000 recovered, split Rs 30,000 / Rs 20,000; Rs 20,000 released to the holder |
| Bank and investigator jointly endorse | Wrong bank's peer refused; Investigator alone failed the endorsement policy |
| Case details private | Bank B's database: 0 rows mentioning either victim's name |

### How it works

- **Tracing.** The receiving bank records each transfer. An onward transfer carries only as much of the victims' money as was still in the account.
- **Pinned endorsers.** The policy is Investigator AND any one of the banks, and the contract refuses to run on any bank's peer other than the one the action concerns.
- **Pro-rata recovery.** Shares follow what each victim lost, never more than was held for them; leftover paise go to the largest remainders so the shares add up exactly.

## Working from Windows

If you edit on Windows and run in WSL, `tools/wsl-sync.sh` copies the working copy at `/mnt/d/GIT/ClearChain` into `~/ClearChain`, converting line endings. Change the `SRC` path at the top if your checkout lives elsewhere.

## What is still open

- **Dashboard.** The proposal's three-view dashboard (bank health and routing, supply-chain flow, fraud tracing and holds) is not built. Every view's data is already queryable in each org's YugabyteDB.
- **Scaling load test.** Throughput as lite peers and validation services are added is not measured. Each org currently runs one of each.
- **Mutual TLS and Sparse Block Delivery.** Both are named in the proposal and not yet checked or configured.
- **Pillar 1 health measurements** are public on the ledger; the proposal puts them in a private collection.
- **Hold-and-retry mode** is implemented and unit-tested but not shown in a live demo.

### Known gaps in the network scripts

- `scripts/deployCCAAS.sh` (chaincode-as-a-service) still handles only Switch and NPCI. Use `deployCC` or `tools/deploy-contracts.sh`.
- `crypto-config/` under `clearchain-test-network` holds unused sample certificates and keys from upstream and can be deleted.
- Database and KeyDB credentials are development defaults.
