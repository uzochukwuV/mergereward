# MergeReward — CRE Architecture

> GitHub-native developer bounties, paid automatically when a PR is merged.

---

## How It Works — End to End

```
GitHub PR merged
      │
      ▼
GitHub Webhook ──► Your Backend API
                         │
                         ▼
                   CRE HTTP Trigger
                         │
                         ▼
              ┌──────────────────────┐
              │   CRE DON Nodes      │
              │                      │
              │  1. Receive event    │
              │  2. Each node calls  │◄──── GITHUB_TOKEN (secret, TEE only)
              │     GitHub REST API  │
              │  3. Verify PR merged │
              │  4. Reach consensus  │
              │     on bountyId +    │
              │     developer addr   │
              └──────────┬───────────┘
                         │
                         ▼
              KeystoneForwarder (on-chain)
                         │
                         ▼
              MergeReward.releaseBounty(bountyId, developer)
                         │
                    ┌────┴────┐
                    │ Escrow  │
                    │ split   │
                    └────┬────┘
               95%       │      5%
                ▼        │       ▼
       developerBalances  │   Protocol
       [developer] +=     │    wallet
       payout             │   (immediate)
                ▼
       Developer calls withdraw()
       when ready — any amount,
       one tx, one gas fee
```

---

## File Structure

```
bring-your-own-data/
├── contracts/                           ← Foundry project (single source of truth)
│   ├── foundry.toml
│   ├── remappings.txt
│   ├── src/
│   │   ├── MergeReward.sol              ← Main escrow + payout contract
│   │   └── util/
│   │       └── Workflow.sol             ← CRE KeystoneForwarder helper
│   └── scripts/
│       └── DeployMergeReward.s.sol      ← Foundry deploy script
│
├── files/                               ← Workflow config & documentation
│   ├── README.md                        ← This file
│   ├── FRONTEND.md                      ← Frontend implementation guide
│   ├── workflow.go                      ← CRE workflow (Go SDK)
│   ├── workflow.yaml                    ← CRE CLI settings
│   ├── config.json                      ← Non-secret config (contract address, chain)
│   └── secrets.yaml                     ← Secret values (NEVER commit — TEE only)
│
└── backend-go/                          ← Go API server
    └── ...
```

---

## Smart Contract: MergeReward.sol

### Key Functions

| Function | Who Calls It | What It Does |
|---|---|---|
| `createBounty(repoId, issueNumber, ttl)` | Maintainer | Locks ETH in escrow for an issue |
| `claimIssue(repoId, issueNumber)` | Developer | Registers intent to fix the issue |
| `releaseBounty(bountyId, developer)` | CRE KeystoneForwarder ONLY | Pays developer after verified merge |
| `cancelBounty(...)` | Maintainer | Refund if no claimer yet |
| `refundExpiredBounty(...)` | Maintainer | Refund after TTL expires with no merge |

### Security Design

- `releaseBounty` reverts if `msg.sender != keystoneForwarder` — no human can trigger it
- The `workflowName` is stored on-chain; the forwarder checks it matches the registered workflow
- BountyId is `keccak256(repoId, issueNumber)` — computed identically in Go and Solidity
- 5% fee deducted at payout — all logic transparent on-chain

---

## CRE Workflow: workflow.go

### What Each Step Does

**Step 1 — HTTP Trigger**
Your backend forwards GitHub `pull_request` webhook events (action: `closed`, merged: `true`) to the CRE workflow HTTP endpoint.

**Step 2 — Confidential HTTP (GitHub API)**
Every DON node independently calls:
```
GET https://api.github.com/repos/{owner}/{repo}/pulls/{pr_number}
```
Using `GITHUB_TOKEN` from CRE secrets (stored encrypted in TEE — never on-chain).
Verifies `pr.merged == true`.

**Step 3 — DON Consensus**
All nodes compute `bountyId = keccak256(repoId, issueNumber)` and must agree on the same `(bountyId, developerAddress)` before proceeding. This is the "trustless" guarantee — no single node can fake a payout.

**Step 4 — EVM Write**
The consensus report is submitted via `KeystoneForwarder → MergeReward.releaseBounty()`.
ETH flows to the developer's wallet. No admin approval. No invoice.

---

## Secrets

Store in `secrets.yaml` (never commit):

| Key | Value |
|---|---|
| `GITHUB_TOKEN` | GitHub PAT with `repo` read scope |
| `NOTIFY_WEBHOOK_URL` | Optional Slack/Discord webhook for notifications |

These are encrypted and available only inside the CRE TEE.

---

## Deploy Checklist

1. `cd contracts && forge install` to fetch dependencies
2. Edit `files/config.json` → set your deployed contract address + chain
3. Edit `files/secrets.yaml` → add your GitHub token (never commit this)
4. Run `forge script scripts/DeployMergeReward.s.sol --rpc-url $RPC_URL --broadcast --verify`
5. Register workflow with CRE CLI: `cre workflow deploy --target staging-settings`
6. Copy the CRE HTTP trigger URL → register it in your backend as `CRE_TRIGGER_URL`

---

## Hackathon Track Alignment

| Track | How MergeReward qualifies |
|---|---|
| **Privacy ($16K)** | GitHub token + payment keys stored in TEE via Confidential HTTP — never on-chain |
| **CRE & AI ($17K)** | Core CRE workflow with DON consensus; extend with LLM PR quality scoring |
| **DeFi & Tokenization ($20K)** | On-chain escrow primitive with automated release = novel DeFi building block |
