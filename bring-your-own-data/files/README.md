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
           Developer   (fee)  Protocol
            wallet            wallet
```

---

## File Structure

```
mergereward/
├── contracts/
│   ├── src/
│   │   ├── MergeReward.sol          ← Main escrow + release contract
│   │   └── util/Workflow.sol        ← CRE helper (from bring-your-own-data template)
│   └── scripts/
│       └── DeployMergeReward.s.sol  ← Foundry deploy script
│
└── workflow-go/
    ├── workflow.go                  ← CRE workflow (Go SDK)
    ├── config.json                  ← Non-secret config (contract address, chain)
    ├── secrets.yaml                 ← Secret values (NEVER commit — TEE only)
    └── workflow.yaml                ← CRE CLI settings
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

1. `cp contracts/src/util/Workflow.sol` from the bring-your-own-data template
2. Edit `config.json` → set your deployed contract address + chain
3. Edit `secrets.yaml` → add your GitHub token
4. Run `forge script scripts/DeployMergeReward.s.sol --broadcast`
5. Register workflow with CRE CLI: `cre workflow deploy --target staging-settings`
6. Copy the CRE HTTP trigger URL → register it in your backend as the GitHub webhook destination

---

## Hackathon Track Alignment

| Track | How MergeReward qualifies |
|---|---|
| **Privacy ($16K)** | GitHub token + payment keys stored in TEE via Confidential HTTP — never on-chain |
| **CRE & AI ($17K)** | Core CRE workflow with DON consensus; extend with LLM PR quality scoring |
| **DeFi & Tokenization ($20K)** | On-chain escrow primitive with automated release = novel DeFi building block |
