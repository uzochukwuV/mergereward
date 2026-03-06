package db

import "database/sql"

// schema is run once at startup; all statements are idempotent.
const schema = `
CREATE TABLE IF NOT EXISTS bounties (
    id                   TEXT        PRIMARY KEY,
    repo_id              TEXT        NOT NULL,
    issue_number         INT         NOT NULL,
    amount_cents         BIGINT      NOT NULL,
    currency             TEXT        NOT NULL DEFAULT 'usd',
    stripe_checkout_id   TEXT,
    status               TEXT        NOT NULL DEFAULT 'pending_payment',
    claimer_github_login TEXT,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS bounties_repo_issue
    ON bounties (repo_id, issue_number);

CREATE TABLE IF NOT EXISTS developers (
    github_login       TEXT        PRIMARY KEY,
    stripe_account_id  TEXT,
    stripe_onboarded   BOOLEAN     NOT NULL DEFAULT FALSE,
    wallet_address     TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_onboarded_at  TIMESTAMPTZ
);

-- idempotent backfill: add column if it was created without wallet_address
ALTER TABLE developers ADD COLUMN IF NOT EXISTS wallet_address TEXT;

CREATE TABLE IF NOT EXISTS merged_prs (
    bounty_id  TEXT        PRIMARY KEY REFERENCES bounties (id),
    repo_id    TEXT        NOT NULL,
    pr_number  INT         NOT NULL,
    sha        TEXT        NOT NULL,
    author     TEXT        NOT NULL,
    merged_at  TIMESTAMPTZ NOT NULL,
    body       TEXT        NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS ai_results (
    bounty_id TEXT    PRIMARY KEY REFERENCES bounties (id),
    score     INT     NOT NULL,
    summary   TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS payouts (
    bounty_id          TEXT        PRIMARY KEY REFERENCES bounties (id),
    stripe_transfer_id TEXT        NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
`

// Migrate applies the schema to the database. Safe to call on every startup.
func Migrate(db *sql.DB) error {
	_, err := db.Exec(schema)
	return err
}
