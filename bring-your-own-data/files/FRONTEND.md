# MergeReward — Frontend Implementation Guide

This document is the single source of truth for a frontend engineer implementing
the MergeReward UI. It covers every API endpoint, smart contract interaction,
WebSocket event, and state machine required to build the full product.

---

## Table of Contents

1. [Architecture Overview](#1-architecture-overview)
2. [Authentication — GitHub OAuth](#2-authentication--github-oauth)
3. [Developer Profile & Wallet Setup](#3-developer-profile--wallet-setup)
4. [Connecting a GitHub Repository](#4-connecting-a-github-repository)
5. [Creating an Issue Bounty (Gig)](#5-creating-an-issue-bounty-gig)
6. [Depositing Funds for a Bounty](#6-depositing-funds-for-a-bounty)
7. [Developer — Browsing & Requesting Issues](#7-developer--browsing--requesting-issues)
8. [Issue Status Reference](#8-issue-status-reference)
9. [PR & Merge Status](#9-pr--merge-status)
10. [On-Chain Balance & Withdrawal](#10-on-chain-balance--withdrawal)
11. [Stripe Payout (Optional)](#11-stripe-payout-optional)
12. [Real-Time Updates via WebSocket](#12-real-time-updates-via-websocket)
13. [Smart Contract ABI Reference](#13-smart-contract-abi-reference)
14. [Environment Variables for the Frontend](#14-environment-variables-for-the-frontend)
15. [Complete Flow Diagrams](#15-complete-flow-diagrams)
16. [Error Handling Reference](#16-error-handling-reference)

---

## 1. Architecture Overview

```
┌──────────────────────────────────────────────────────────┐
│                     Frontend (React / Next.js)           │
│                                                          │
│  Auth ──► GitHub OAuth ──► Session Cookie (JWT)         │
│                                                          │
│  Maintainer ──► POST /bounties ──► Stripe Checkout  ─┐  │
│                               └──► On-Chain Params  ─┤  │
│                                                      │  │
│  Developer  ──► POST /bounties/claim              ◄──┘  │
│             ──► ethers.js claimIssue() (contract)       │
│             ──► ethers.js withdraw()  (contract)        │
│                                                          │
│  Real-time ──► WebSocket /ws                            │
└──────────────────────────────────────────────────────────┘
         │                               │
         │ HTTP                          │ on-chain
         ▼                               ▼
   MergeReward Backend           MergeReward.sol
   (Go, port 8080)               (Ethereum / Base Sepolia)
```

**Key concepts:**

| Concept | Description |
|---------|-------------|
| **Bounty** | ETH or fiat reward locked for a specific GitHub issue |
| **Maintainer** | The person who creates + funds a bounty |
| **Developer** | The person who solves the issue and earns the bounty |
| **CRE** | Chainlink Runtime Environment — verifies PR merges on-chain |
| **Fund holding** | Earned ETH accumulates in the contract; developers withdraw when ready |
| **Stripe (optional)** | Fiat payment path; not required when using on-chain ETH |

---

## 2. Authentication — GitHub OAuth

### Flow

```
1.  GET  /auth/github          ──► redirect to GitHub
2.  GitHub callback            ──► GET /auth/github/callback
3.  Backend sets HttpOnly JWT cookie
4.  Redirect to FRONTEND_URL   ──► frontend reads session via GET /auth/me
```

### Step-by-step

**Step 1: Start OAuth**

```ts
// Redirect the user to begin GitHub OAuth
window.location.href = `${API_BASE}/auth/github`;
```

**Step 2: After redirect back, fetch user**

The backend redirects to your `FRONTEND_URL` (e.g. `/auth/callback` or `/`).
The session cookie is set automatically. Then:

```ts
// GET /auth/me
const res = await fetch(`${API_BASE}/auth/me`, { credentials: 'include' });
// 200 → logged in, 401 → not logged in
```

**Response (200):**

```json
{
  "githubLogin": "alice",
  "githubId": 12345678,
  "stripeOnboarded": false,
  "stripeEnabled": false,
  "paymentMode": "onchain",
  "walletAddress": ""
}
```

| Field | Type | Notes |
|-------|------|-------|
| `githubLogin` | string | GitHub username |
| `githubId` | number | GitHub user ID |
| `stripeEnabled` | boolean | `true` only if backend has Stripe configured |
| `stripeOnboarded` | boolean | `true` if developer completed Stripe Connect |
| `paymentMode` | `"stripe"` \| `"onchain"` | Active payment mode for this deployment |
| `walletAddress` | string | EVM wallet address (empty if not set yet) |

**Step 3: Full developer profile**

```ts
// GET /developers/me  (requires session cookie)
const res = await fetch(`${API_BASE}/developers/me`, { credentials: 'include' });
```

Returns the same shape as `/auth/me`. Use this for the dashboard.

**Step 4: Logout**

```ts
await fetch(`${API_BASE}/auth/logout`, { method: 'POST', credentials: 'include' });
```

---

## 3. Developer Profile & Wallet Setup

Developers need to save their EVM wallet address so the system can display their
on-chain balance and guide them to call `withdraw()` from the right address.

**Save wallet address:**

```ts
// PATCH /developers/me/wallet  (requires session cookie)
const res = await fetch(`${API_BASE}/developers/me/wallet`, {
  method: 'PATCH',
  credentials: 'include',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ walletAddress: '0xYourAddress' }),
});
```

**Request body:**

```json
{ "walletAddress": "0xAbCd...1234" }
```

- Must be `0x` + 40 hex characters (EVM address format)
- Case-insensitive hex (checksum format preferred)

**Response (200):**

```json
{ "status": "ok", "walletAddress": "0xAbCd...1234" }
```

**UI guidance:**
- Show wallet setup step during onboarding for on-chain payment mode
- If `paymentMode === "onchain"`, this is essential for the developer to call `withdraw()`
- The contract's `getBalance(address)` is the source of truth for pending earnings

---

## 4. Connecting a GitHub Repository

The backend discovers repositories via GitHub webhooks. There is no dedicated
"connect repo" API — instead, guide the maintainer through GitHub webhook setup.

### What to show in the UI

**Step 1 — Maintainer enters their GitHub repo**

Show an input for `owner/repo` (e.g. `acme-org/my-project`).

**Step 2 — Generate the webhook URL**

```
https://your-backend.example.com/webhooks/github
```

Instruct the maintainer to go to:
```
GitHub → Repository → Settings → Webhooks → Add webhook
  Payload URL: https://your-backend.example.com/webhooks/github
  Content type: application/json
  Secret: <GITHUB_WEBHOOK_SECRET from backend config>
  Events: ✅ Pull requests
```

**Step 3 — Confirm**

Once the webhook is added, the backend will receive `pull_request` events
for merged PRs. Show a "webhook connected" confirmation — you can verify by
watching the `/ws` WebSocket for any `pr.merged` events from that repo.

### Repository Data Model (frontend only)

```ts
interface Repository {
  fullName: string;   // "owner/repo"
  webhookConnected: boolean;
  createdAt: string;
}
```

Store this in local state or your own DB — the backend does not persist repos
independently. The link to bounties is via `repoId` (= `owner/repo`).

---

## 5. Creating an Issue Bounty (Gig)

Maintainers link a bounty to a specific GitHub issue and deposit funds.

### API: Create Bounty

```
POST /bounties
Content-Type: application/json
```

**Request body:**

```json
{
  "repoId": "acme-org/my-project",
  "issueNumber": 42,
  "amountCents": 5000,
  "currency": "usd",
  "successUrl": "https://app.example.com/bounties/success",
  "cancelUrl": "https://app.example.com/bounties/cancel"
}
```

| Field | Required | Notes |
|-------|----------|-------|
| `repoId` | Yes | GitHub `owner/repo` format |
| `issueNumber` | Yes | GitHub issue number |
| `amountCents` | Yes | Amount in smallest currency unit (e.g. cents for USD) |
| `currency` | Yes | ISO 4217 code (`"usd"`) |
| `successUrl` | Conditional | Required only when `stripeEnabled === true` |
| `cancelUrl` | Conditional | Required only when `stripeEnabled === true` |

**Response (201) — Stripe mode:**

```json
{
  "bountyId": "bty_abc123",
  "paymentMode": "stripe",
  "checkoutUrl": "https://checkout.stripe.com/...",
  "checkoutId": "cs_live_...",
  "paymentStatus": "pending_payment"
}
```

**Response (201) — On-chain mode:**

```json
{
  "bountyId": "bty_abc123",
  "paymentMode": "onchain",
  "paymentStatus": "funded"
}
```

### UI Branch by Payment Mode

```ts
const { bountyId, paymentMode, checkoutUrl } = await createBounty(req);

if (paymentMode === 'stripe') {
  // Redirect to Stripe checkout
  window.location.href = checkoutUrl;
} else {
  // paymentMode === 'onchain'
  // Guide maintainer to call createBounty() on the smart contract
  showOnChainFundingModal({ bountyId, repoId, issueNumber, amountEth });
}
```

---

## 6. Depositing Funds for a Bounty

### Stripe Mode

After `POST /bounties` returns a `checkoutUrl`, redirect the user there.
Stripe handles payment. When complete, Stripe calls the backend webhook and
the bounty status updates to `funded`. Your `successUrl` page should:

```ts
// Poll or listen via WebSocket for status change
// GET /bounties/{id}
const bounty = await fetch(`${API_BASE}/bounties/${bountyId}`, { credentials: 'include' }).then(r => r.json());
if (bounty.status === 'funded') {
  showSuccess();
}
```

### On-Chain Mode (ETH via smart contract)

When `paymentMode === "onchain"`, the maintainer funds the bounty directly on
the Ethereum/Base Sepolia smart contract.

**Using ethers.js / wagmi:**

```ts
import { ethers } from 'ethers';
import { MERGE_REWARD_ABI } from './abi';

const CONTRACT_ADDRESS = '0xYourDeployedContractAddress';

async function fundBounty(repoId: string, issueNumber: number, ethAmount: string) {
  const provider = new ethers.BrowserProvider(window.ethereum);
  const signer = await provider.getSigner();
  const contract = new ethers.Contract(CONTRACT_ADDRESS, MERGE_REWARD_ABI, signer);

  // ttlSeconds: how long before maintainer can refund (e.g. 30 days)
  const ttlSeconds = 30 * 24 * 60 * 60;

  const tx = await contract.createBounty(repoId, issueNumber, ttlSeconds, {
    value: ethers.parseEther(ethAmount),
  });

  await tx.wait(); // Wait for confirmation
  console.log('Bounty funded on-chain:', tx.hash);
}
```

**Show the maintainer:**
1. The contract address to interact with
2. The `repoId` (e.g. `acme-org/my-project`) and `issueNumber` to pass
3. How much ETH to send (convert from your displayed USD equivalent using a price feed)
4. A transaction confirmation UI (MetaMask or WalletConnect)

---

## 7. Developer — Browsing & Requesting Issues

### List Bounties for a Repo/Issue

```
GET /bounties?repoId=owner/repo&issueNumber=42
```

**Response (200):**

```json
{
  "bounty": {
    "id": "bty_abc123",
    "repoId": "acme-org/my-project",
    "issueNumber": 42,
    "amountCents": 5000,
    "currency": "usd",
    "status": "funded",
    "claimerGithubLogin": "",
    "createdAt": "2026-03-01T10:00:00Z",
    "mergedPr": null,
    "payout": null
  }
}
```

If no bounty exists: `{ "bounty": null }`

### Get Specific Bounty

```
GET /bounties/{id}
```

Returns the same `Bounty` object.

### Claim a Bounty (Request to Work on Issue)

A developer signals intent to work on the issue. One claim per bounty.

```
POST /bounties/claim
Authorization: Session cookie required
Content-Type: application/json
```

**Request body:**

```json
{ "bountyId": "bty_abc123" }
```

**Response (200):**

```json
{ "status": "ok" }
```

**Errors:**

| Status | Body | Meaning |
|--------|------|---------|
| 401 | `{"error":"not authenticated"}` | Must be logged in |
| 400 | `{"error":"bountyId is required"}` | Missing field |
| 409 | `{"error":"already claimed"}` | Someone else already claimed |
| 404 | `{"error":"not found"}` | Bounty doesn't exist |

**On-chain claim (optional, gas required):**

For full on-chain proof of claim, the developer can also call `claimIssue()`
on the smart contract. The backend's off-chain claim is sufficient for the CRE
workflow, but the on-chain claim provides an immutable record.

```ts
const tx = await contract.claimIssue(repoId, issueNumber);
await tx.wait();
```

---

## 8. Issue Status Reference

A bounty moves through these states. Use `status` from the `GET /bounties/{id}`
response to drive your UI.

| Status | `paymentMode` | Description | UI Action |
|--------|---------------|-------------|-----------|
| `pending_payment` | stripe | Bounty created, awaiting Stripe payment | Show "Complete payment" button |
| `funded` | any | Bounty funded, open for developers to claim | Show "Claim this issue" button |
| `funded` + `claimerGithubLogin` set | any | Issue assigned, developer working | Show assignee badge |
| `funded` + `mergedPr` set | any | PR merged, CRE verifying | Show "Awaiting verification" |
| `paid` | stripe | Stripe transfer sent to developer | Show "Paid via Stripe" |
| `paid` | onchain | On-chain `releaseBounty` called, balance credited | Show "Earned — ready to withdraw" |

**Derived status for display:**

```ts
function getBountyDisplayStatus(bounty: Bounty): string {
  if (bounty.status === 'paid') {
    return bounty.payout?.stripeTransferId
      ? 'Paid via Stripe'
      : 'Earned — withdraw from contract';
  }
  if (bounty.mergedPr) return 'PR merged — verifying';
  if (bounty.claimerGithubLogin) return `Claimed by @${bounty.claimerGithubLogin}`;
  if (bounty.status === 'funded') return 'Open — ready to claim';
  return 'Awaiting payment';
}
```

---

## 9. PR & Merge Status

### How PR Linking Works

The backend detects merges by watching GitHub webhooks. A PR is automatically
linked to a bounty if its body contains one of these patterns:

```
Fixes #42
Closes #42
Resolves #42
Fix #42
Close #42
Resolve #42
```

(Case-insensitive, with or without colon, spaces around `#`)

**Guide developers to include this in their PR description:**

> _"Closes #42"_ — link your PR to the issue to trigger automatic payout.

### PR Status Fields in Bounty Object

```ts
interface MergedPR {
  repoId: string;
  prNumber: number;
  sha: string;
  author: string;       // GitHub login of PR author
  mergedAt: string;     // ISO 8601 timestamp
  body: string;         // PR description body
}

interface Bounty {
  // ...
  mergedPr: MergedPR | null;   // null until PR is merged
  payout: Payout | null;       // null until payout recorded
}
```

### Assigned Status

```ts
// Show assignment info
const isAssigned = bounty.claimerGithubLogin !== '';
const isAssignedToMe = bounty.claimerGithubLogin === currentUser.githubLogin;
```

### Merge + Verification Status

```ts
type MergeStatus =
  | 'not_merged'          // mergedPr === null
  | 'merged_verifying'    // mergedPr set, payout === null
  | 'verified_paid';      // payout set

function getMergeStatus(bounty: Bounty): MergeStatus {
  if (!bounty.mergedPr) return 'not_merged';
  if (!bounty.payout)   return 'merged_verifying';
  return 'verified_paid';
}
```

---

## 10. On-Chain Balance & Withdrawal

When `paymentMode === "onchain"`, earned ETH accumulates inside the
`MergeReward` contract under the developer's address. Developers withdraw
when they're ready — once, for all accumulated earnings — paying gas only once.

### Reading the On-Chain Balance

```ts
import { ethers } from 'ethers';

async function getDeveloperBalance(walletAddress: string): Promise<string> {
  const provider = new ethers.JsonRpcProvider(RPC_URL);
  const contract = new ethers.Contract(CONTRACT_ADDRESS, MERGE_REWARD_ABI, provider);

  const balanceWei: bigint = await contract.getBalance(walletAddress);
  return ethers.formatEther(balanceWei); // Returns ETH as string, e.g. "0.095"
}
```

Show this prominently on the developer dashboard:

```
┌─────────────────────────────────┐
│  Your pending earnings          │
│                                 │
│  0.095 ETH                      │
│  ≈ $285.00 USD                  │
│                                 │
│  [Withdraw to wallet]           │
└─────────────────────────────────┘
```

### Withdrawing Funds

**Option A — Withdraw to connected wallet:**

```ts
async function withdraw() {
  const provider = new ethers.BrowserProvider(window.ethereum);
  const signer = await provider.getSigner();
  const contract = new ethers.Contract(CONTRACT_ADDRESS, MERGE_REWARD_ABI, signer);

  const tx = await contract.withdraw();
  await tx.wait();
  console.log('Withdrawn!', tx.hash);
}
```

**Option B — Withdraw to a different address:**

```ts
async function withdrawTo(recipientAddress: string) {
  const provider = new ethers.BrowserProvider(window.ethereum);
  const signer = await provider.getSigner();
  const contract = new ethers.Contract(CONTRACT_ADDRESS, MERGE_REWARD_ABI, signer);

  const tx = await contract.withdrawTo(recipientAddress);
  await tx.wait();
}
```

**Error handling:**

| Contract Error | User Message |
|---------------|--------------|
| `NothingToWithdraw` | "No earnings to withdraw yet" |
| `WithdrawFailed` | "Transfer failed — please try again" |
| `Unauthorized` | "Invalid recipient address" |

### Listening for On-Chain Events

```ts
// Listen for DeveloperFunded (earnings credited after PR merged)
contract.on('DeveloperFunded', (developer, bountyId, amount, newBalance) => {
  if (developer.toLowerCase() === userAddress.toLowerCase()) {
    console.log(`Earned ${ethers.formatEther(amount)} ETH!`);
    console.log(`New balance: ${ethers.formatEther(newBalance)} ETH`);
  }
});

// Listen for DeveloperWithdrew
contract.on('DeveloperWithdrew', (developer, recipient, amount) => {
  if (developer.toLowerCase() === userAddress.toLowerCase()) {
    console.log(`Withdrew ${ethers.formatEther(amount)} ETH to ${recipient}`);
  }
});
```

---

## 11. Stripe Payout (Optional)

This section only applies when `stripeEnabled === true` (backend has
`STRIPE_SECRET_KEY` + `STRIPE_WEBHOOK_SECRET` configured).

### Developer Onboarding

Before a developer can receive Stripe payments they must complete Stripe Connect
Express onboarding.

```
POST /developers/stripe/onboard
Content-Type: application/json
```

**Request body:**

```json
{
  "githubLogin": "alice",
  "returnUrl": "https://app.example.com/stripe/return",
  "refreshUrl": "https://app.example.com/stripe/refresh"
}
```

**Response (200):**

```json
{
  "stripeAccountId": "acct_1234",
  "onboardingUrl": "https://connect.stripe.com/setup/..."
}
```

Redirect the developer to `onboardingUrl`. After they complete onboarding,
Stripe redirects them back to `returnUrl`.

On return, check `/developers/me` — `stripeOnboarded` will be `true` once
Stripe has confirmed via webhook.

### Payout Flow (Stripe)

When a PR is merged and verified by CRE, the backend automatically initiates a
Stripe Connect transfer to the developer. No frontend action required — you just
need to show status updates via WebSocket or polling.

---

## 12. Real-Time Updates via WebSocket

Connect to `/ws` for real-time push notifications. All events are broadcast to
all connected clients.

```ts
const ws = new WebSocket(`wss://your-backend.example.com/ws`);

ws.onmessage = (event) => {
  const msg: WSEvent = JSON.parse(event.data);
  handleEvent(msg.type, msg.data);
};
```

### Event Reference

| Event Type | Description | Data Fields |
|-----------|-------------|-------------|
| `pr.merged` | A PR was merged and matched to a bounty | `bountyId`, `repoId`, `issueNumber`, `prNumber`, `author`, `mergeCommit`, `mergedAt`, `bountyStatus` |
| `pr.merged.unmatched` | PR merged but no matching bounty found | `repoId`, `issueNumber`, `prNumber` |
| `pr.merged.unclaimed` | PR merged but bounty has no claimer | `bountyId`, `author` |
| `pr.merged.claimer_mismatch` | PR author != claimer | `bountyId`, `claimer`, `author` |
| `bounty.claimed` | Developer claimed a bounty | `bountyId`, `githubLogin` |
| `bounty.paid` | Bounty payout recorded | `bountyId`, `developer`, `amountCents`, `currency`, `paymentMode` (+ `stripeTransferId` if stripe) |
| `developer.stripe.onboard` | Developer started Stripe onboarding | `githubLogin`, `stripeAccountId` |
| `cre.forwarded` | Merge event sent to CRE | `bountyId`, `status` (HTTP status) |
| `cre.error` | CRE forwarding failed | `bountyId`, `error` |
| `cre.config_missing` | CRE trigger not configured | `bountyId`, `error` |
| `ai.result` | AI PR quality score available | `bountyId`, `score` (0–100), `summary` |
| `ai.error` | AI scoring failed | `bountyId`, `error` |

### Example Handler

```ts
function handleEvent(type: string, data: Record<string, any>) {
  switch (type) {
    case 'pr.merged':
      toast.info(`PR #${data.prNumber} merged for bounty ${data.bountyId}`);
      invalidateBountyCache(data.bountyId);
      break;

    case 'bounty.paid':
      if (data.paymentMode === 'onchain') {
        toast.success(`Earnings credited on-chain! Check your balance.`);
      } else {
        toast.success(`$${(data.amountCents / 100).toFixed(2)} sent via Stripe!`);
      }
      invalidateBountyCache(data.bountyId);
      break;

    case 'bounty.claimed':
      invalidateBountyCache(data.bountyId);
      break;

    case 'ai.result':
      setBountyAIScore(data.bountyId, data.score, data.summary);
      break;
  }
}
```

---

## 13. Smart Contract ABI Reference

Deploy address is set by your infrastructure team. Contract is deployed on
Ethereum Sepolia and/or Base Sepolia.

### Key Functions

```ts
export const MERGE_REWARD_ABI = [
  // ── Maintainer ──────────────────────────────────────────────────────────
  {
    name: 'createBounty',
    type: 'function',
    stateMutability: 'payable',
    inputs: [
      { name: 'repoId', type: 'string' },
      { name: 'issueNumber', type: 'uint256' },
      { name: 'ttlSeconds', type: 'uint256' },
    ],
    outputs: [],
  },
  {
    name: 'cancelBounty',
    type: 'function',
    stateMutability: 'nonpayable',
    inputs: [
      { name: 'repoId', type: 'string' },
      { name: 'issueNumber', type: 'uint256' },
    ],
    outputs: [],
  },
  {
    name: 'refundExpiredBounty',
    type: 'function',
    stateMutability: 'nonpayable',
    inputs: [
      { name: 'repoId', type: 'string' },
      { name: 'issueNumber', type: 'uint256' },
    ],
    outputs: [],
  },

  // ── Developer ───────────────────────────────────────────────────────────
  {
    name: 'claimIssue',
    type: 'function',
    stateMutability: 'nonpayable',
    inputs: [
      { name: 'repoId', type: 'string' },
      { name: 'issueNumber', type: 'uint256' },
    ],
    outputs: [],
  },
  {
    name: 'withdraw',
    type: 'function',
    stateMutability: 'nonpayable',
    inputs: [],
    outputs: [],
  },
  {
    name: 'withdrawTo',
    type: 'function',
    stateMutability: 'nonpayable',
    inputs: [{ name: 'recipient', type: 'address' }],
    outputs: [],
  },

  // ── Views ────────────────────────────────────────────────────────────────
  {
    name: 'getBounty',
    type: 'function',
    stateMutability: 'view',
    inputs: [
      { name: 'repoId', type: 'string' },
      { name: 'issueNumber', type: 'uint256' },
    ],
    outputs: [
      {
        name: '',
        type: 'tuple',
        components: [
          { name: 'maintainer', type: 'address' },
          { name: 'amount', type: 'uint256' },
          { name: 'claimer', type: 'address' },
          { name: 'released', type: 'bool' },
          { name: 'refunded', type: 'bool' },
          { name: 'expiresAt', type: 'uint256' },
        ],
      },
    ],
  },
  {
    name: 'getBountyById',
    type: 'function',
    stateMutability: 'view',
    inputs: [{ name: 'bountyId', type: 'bytes32' }],
    outputs: [
      {
        name: '',
        type: 'tuple',
        components: [
          { name: 'maintainer', type: 'address' },
          { name: 'amount', type: 'uint256' },
          { name: 'claimer', type: 'address' },
          { name: 'released', type: 'bool' },
          { name: 'refunded', type: 'bool' },
          { name: 'expiresAt', type: 'uint256' },
        ],
      },
    ],
  },
  {
    name: 'getBalance',
    type: 'function',
    stateMutability: 'view',
    inputs: [{ name: 'developer', type: 'address' }],
    outputs: [{ name: '', type: 'uint256' }],
  },
  {
    name: 'developerBalances',
    type: 'function',
    stateMutability: 'view',
    inputs: [{ name: '', type: 'address' }],
    outputs: [{ name: '', type: 'uint256' }],
  },
  {
    name: 'FEE_BPS',
    type: 'function',
    stateMutability: 'view',
    inputs: [],
    outputs: [{ name: '', type: 'uint256' }],
  },

  // ── Events ───────────────────────────────────────────────────────────────
  {
    name: 'BountyCreated',
    type: 'event',
    inputs: [
      { name: 'bountyId', type: 'bytes32', indexed: true },
      { name: 'repoId', type: 'string', indexed: false },
      { name: 'issueNumber', type: 'uint256', indexed: false },
      { name: 'maintainer', type: 'address', indexed: true },
      { name: 'amount', type: 'uint256', indexed: false },
      { name: 'expiresAt', type: 'uint256', indexed: false },
    ],
  },
  {
    name: 'IssueClaimed',
    type: 'event',
    inputs: [
      { name: 'bountyId', type: 'bytes32', indexed: true },
      { name: 'claimer', type: 'address', indexed: true },
    ],
  },
  {
    name: 'BountyReleased',
    type: 'event',
    inputs: [
      { name: 'bountyId', type: 'bytes32', indexed: true },
      { name: 'developer', type: 'address', indexed: true },
      { name: 'payout', type: 'uint256', indexed: false },
      { name: 'fee', type: 'uint256', indexed: false },
    ],
  },
  {
    name: 'DeveloperFunded',
    type: 'event',
    inputs: [
      { name: 'developer', type: 'address', indexed: true },
      { name: 'bountyId', type: 'bytes32', indexed: true },
      { name: 'amount', type: 'uint256', indexed: false },
      { name: 'newBalance', type: 'uint256', indexed: false },
    ],
  },
  {
    name: 'DeveloperWithdrew',
    type: 'event',
    inputs: [
      { name: 'developer', type: 'address', indexed: true },
      { name: 'recipient', type: 'address', indexed: true },
      { name: 'amount', type: 'uint256', indexed: false },
    ],
  },
  {
    name: 'BountyCancelled',
    type: 'event',
    inputs: [
      { name: 'bountyId', type: 'bytes32', indexed: true },
      { name: 'maintainer', type: 'address', indexed: true },
      { name: 'amount', type: 'uint256', indexed: false },
    ],
  },
  {
    name: 'BountyRefunded',
    type: 'event',
    inputs: [
      { name: 'bountyId', type: 'bytes32', indexed: true },
      { name: 'maintainer', type: 'address', indexed: true },
      { name: 'amount', type: 'uint256', indexed: false },
    ],
  },
] as const;
```

### Computing BountyId (JavaScript)

The contract computes `bountyId = keccak256(abi.encodePacked(repoId, issueNumber))`.
To compute the same value client-side:

```ts
import { ethers } from 'ethers';

function computeBountyId(repoId: string, issueNumber: number): string {
  return ethers.solidityPackedKeccak256(
    ['string', 'uint256'],
    [repoId, issueNumber]
  );
}
```

---

## 14. Environment Variables for the Frontend

```env
# Backend API base URL (no trailing slash)
NEXT_PUBLIC_API_BASE=https://api.mergereward.example.com

# Smart contract address (Ethereum Sepolia or Base Sepolia)
NEXT_PUBLIC_CONTRACT_ADDRESS=0x...

# Chain ID for the target network
# Ethereum Sepolia: 11155111
# Base Sepolia: 84532
NEXT_PUBLIC_CHAIN_ID=11155111

# RPC URL for read-only contract calls (no MetaMask required for reads)
NEXT_PUBLIC_RPC_URL=https://ethereum-sepolia-rpc.publicnode.com

# WalletConnect project ID (for mobile wallet support)
NEXT_PUBLIC_WALLETCONNECT_PROJECT_ID=your_project_id
```

---

## 15. Complete Flow Diagrams

### Maintainer Flow (On-Chain)

```
Maintainer
   │
   ├─ 1. GET /auth/github → OAuth → session cookie
   │
   ├─ 2. POST /bounties
   │      { repoId, issueNumber, amountCents, currency }
   │      → { bountyId, paymentMode: "onchain", paymentStatus: "funded" }
   │
   ├─ 3. Connect wallet (MetaMask/WalletConnect)
   │
   └─ 4. contract.createBounty(repoId, issueNumber, ttlSeconds, { value: ethAmount })
          → on-chain bounty created, ETH locked
```

### Developer Flow (On-Chain)

```
Developer
   │
   ├─ 1. GET /auth/github → OAuth → session cookie
   │
   ├─ 2. PATCH /developers/me/wallet { walletAddress: "0x..." }
   │
   ├─ 3. GET /bounties?repoId=...&issueNumber=...
   │      → see open bounty
   │
   ├─ 4. POST /bounties/claim { bountyId }
   │      → claim registered off-chain
   │
   ├─ 5. (optional) contract.claimIssue(repoId, issueNumber)
   │      → on-chain claim
   │
   ├─ 6. Submit PR to GitHub
   │      PR body must contain "Closes #<issueNumber>"
   │
   ├─ 7. PR gets merged → GitHub webhook → backend detects merge
   │      → ws event: "pr.merged"
   │
   ├─ 8. CRE verifies merge (via GitHub API consensus)
   │      → contract.releaseBounty(bountyId, developerAddress)
   │      → developerBalances[address] += payout
   │      → ws event: "bounty.paid" { paymentMode: "onchain" }
   │
   └─ 9. Developer calls contract.withdraw() when ready
          (accumulate multiple bounties → single withdrawal)
          → ETH transferred to wallet
```

### Maintainer Flow (Stripe)

```
Maintainer
   │
   ├─ 1. GET /auth/github → session
   │
   ├─ 2. POST /bounties
   │      { repoId, issueNumber, amountCents, currency, successUrl, cancelUrl }
   │      → { bountyId, paymentMode: "stripe", checkoutUrl }
   │
   └─ 3. window.location.href = checkoutUrl
          → Stripe hosted checkout → payment complete
          → backend webhook: status = "funded"
```

### Developer Flow (Stripe)

```
Developer
   │
   ├─ 1. Auth (same as on-chain)
   │
   ├─ 2. POST /developers/stripe/onboard
   │      → { onboardingUrl }
   │      → redirect to Stripe Connect onboarding
   │
   ├─ 3. Claim + submit PR (same as on-chain steps 4–6)
   │
   └─ 4. PR merged → CRE verifies → POST /internal/payout
          → Stripe transfer to developer's Connect account
          → ws event: "bounty.paid" { paymentMode: "stripe", stripeTransferId }
```

---

## 16. Error Handling Reference

### HTTP API Errors

All errors return JSON:
```json
{ "error": "description of the problem" }
```

| Status | When |
|--------|------|
| 400 | Missing/invalid request fields |
| 401 | Not authenticated (missing or expired session) |
| 404 | Resource not found |
| 409 | Conflict (bounty already claimed, already paid, etc.) |
| 500 | Server misconfiguration (missing env vars) |
| 502 | Upstream service failed (Stripe, GitHub) |
| 503 | Feature not available (Stripe not configured) |

### Smart Contract Errors (custom errors)

| Error | Selector | User Message |
|-------|----------|--------------|
| `BountyAlreadyExists()` | — | "A bounty already exists for this issue" |
| `BountyNotFound()` | — | "Bounty not found" |
| `BountyAlreadySettled()` | — | "Bounty has already been paid or refunded" |
| `Unauthorized()` | — | "You are not authorized for this action" |
| `NotExpiredYet()` | — | "Bounty has not expired yet — refund not available" |
| `AlreadyClaimed()` | — | "This issue is already claimed" |
| `ZeroAmount()` | — | "Must send ETH to create a bounty" |
| `NothingToWithdraw()` | — | "No earnings to withdraw" |
| `WithdrawFailed()` | — | "Withdrawal failed — please try again" |

### Handling Contract Errors with ethers.js

```ts
import { ethers } from 'ethers';

try {
  const tx = await contract.withdraw();
  await tx.wait();
} catch (err: any) {
  if (err.code === 'CALL_EXCEPTION') {
    const errorName = contract.interface.parseError(err.data)?.name;
    switch (errorName) {
      case 'NothingToWithdraw':
        alert('No earnings to withdraw yet.');
        break;
      case 'WithdrawFailed':
        alert('Withdrawal failed — network issue, please retry.');
        break;
      default:
        alert(`Contract error: ${errorName}`);
    }
  } else if (err.code === 'ACTION_REJECTED') {
    // User cancelled in MetaMask
    console.log('User rejected transaction');
  } else {
    console.error(err);
  }
}
```

---

## Quick Reference — All API Endpoints

| Method | Path | Auth | Purpose |
|--------|------|------|---------|
| `GET` | `/healthz` | None | Health check |
| `GET` | `/auth/github` | None | Start GitHub OAuth |
| `GET` | `/auth/github/callback` | None | OAuth callback (backend only) |
| `GET` | `/auth/me` | Optional | Current user info |
| `POST` | `/auth/logout` | None | Clear session |
| `GET` | `/developers/me` | Required | Full developer profile |
| `PATCH` | `/developers/me/wallet` | Required | Save EVM wallet address |
| `POST` | `/developers/stripe/onboard` | None | Start Stripe Connect onboarding |
| `GET` | `/bounties` | None | Find bounty by `?repoId=&issueNumber=` |
| `POST` | `/bounties` | None | Create bounty (+ Stripe checkout if enabled) |
| `GET` | `/bounties/{id}` | None | Get bounty by ID |
| `POST` | `/bounties/claim` | Required | Developer claims a bounty |
| `GET` | `/ws` | None | WebSocket connection |
| `POST` | `/webhooks/github` | HMAC | GitHub webhook (backend only) |
| `POST` | `/webhooks/stripe` | Signature | Stripe webhook (backend only) |

*Endpoints marked "backend only" are called by GitHub/Stripe/CRE, not the frontend.*

---

*This document reflects the state of the codebase as of March 2026.*
*The MergeReward contract is for demo purposes — not audited for production use.*
