# MergeReward Backend (Go)

Minimal Go backend that:

- Receives GitHub `pull_request` webhooks, verifies signatures, and detects merges
- Extracts linked issue numbers from PR body (e.g. `Fixes #123`)
- Broadcasts events to connected clients over WebSocket (`/ws`)
- Creates Stripe Checkout sessions for funding bounties, and handles Stripe webhooks
- Optionally runs an LLM scoring step (OpenAI) and broadcasts the result

## Run

```bash
cd bring-your-own-data/backend-go

go test ./...

go run ./cmd/server
```

## Env vars

### GitHub

- `GITHUB_WEBHOOK_SECRET` (required): webhook signing secret

### Stripe

- `STRIPE_SECRET_KEY` (required for `/bounties`)
- `STRIPE_WEBHOOK_SECRET` (required for `/webhooks/stripe`)

### AI (optional)

- `OPENAI_API_KEY` (enables AI scoring)
- `OPENAI_MODEL` (default: `gpt-4o-mini`)

## Endpoints

- `POST /bounties` -> returns Stripe Checkout URL for funding a bounty
- `POST /webhooks/github`
- `POST /webhooks/stripe`
- `GET /ws`
- `GET /healthz`

WebSocket message format:

```json
{ "type": "pr.merged", "data": { "bountyId": "..." } }
```
