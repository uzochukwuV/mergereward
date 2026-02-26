# MergeReward Backend (Go)

Go backend that:

- Receives GitHub `pull_request` webhooks, verifies signatures, detects merges
- Extracts linked issue numbers from PR body (e.g. `Fixes #123`)
- Forwards merge events to a CRE HTTP trigger (`CRE_TRIGGER_URL`)
- Provides a payout endpoint (`/internal/payout`) for CRE to call via Confidential HTTP
- Stripe:
  - Checkout session to fund bounties
  - Stripe Connect Express onboarding for developers
  - Transfer to developer connected account after CRE verification
- Broadcasts events over WebSocket (`/ws`)
- Optional AI scoring (OpenAI)

## Run

```bash
cd bring-your-own-data/backend-go

go test ./...

go run ./cmd/server
```

## Env vars

### GitHub

- `GITHUB_WEBHOOK_SECRET` (required)

### CRE forwarding (backend -> CRE trigger)

- `CRE_TRIGGER_URL` (required for forwarding)
- `CRE_TRIGGER_TOKEN` (required for forwarding; backend sends `Authorization: Bearer <token>`)

### CRE -> backend payout auth

- `CRE_BACKEND_TOKEN` (required for `/internal/payout`; CRE sends `Authorization: Bearer <token>`)

### Stripe

- `STRIPE_SECRET_KEY` (required)
- `STRIPE_WEBHOOK_SECRET` (required)

### AI (optional)

- `OPENAI_API_KEY` (enables AI scoring)
- `OPENAI_MODEL` (default: `gpt-4o-mini`)

## Endpoints

- `POST /bounties` -> create bounty + Stripe Checkout URL
- `POST /bounties/claim` -> claim bounty (GitHub OAuth login is source of truth)
- `POST /developers/stripe/onboard` -> create/reuse Connect Express account + onboarding link
- `POST /webhooks/github`
- `POST /webhooks/stripe`
- `POST /internal/payout` (CRE-only)
- `GET /ws`
- `GET /healthz`

WebSocket message format:

```json
{ "type": "pr.merged", "data": { "bountyId": "..." } }
```
