package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	_ "github.com/lib/pq"

	"mergereward-backend/internal/store"
)

// Postgres implements store.Store backed by PostgreSQL.
type Postgres struct {
	db *sql.DB
}

// Open connects to Postgres, runs migrations, and returns a ready store.
func Open(dsn string) (*Postgres, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := Migrate(db); err != nil {
		return nil, err
	}
	return &Postgres{db: db}, nil
}

// Close closes the underlying connection pool.
func (p *Postgres) Close() error { return p.db.Close() }

// ─── Bounty helpers ──────────────────────────────────────────────────────────

// scanBounty assembles a full *store.Bounty from a row returned by bountyQuery.
func scanBounty(row *sql.Row) (*store.Bounty, error) {
	var b store.Bounty
	var checkoutID, claimer sql.NullString

	// merged_pr columns
	var mpRepoID, mpSHA, mpAuthor, mpBody sql.NullString
	var mpPRNumber sql.NullInt32
	var mpMergedAt sql.NullTime

	// ai_result columns
	var aiScore sql.NullInt32
	var aiSummary sql.NullString

	// payout columns
	var payTransferID sql.NullString
	var payCreatedAt sql.NullTime

	err := row.Scan(
		&b.ID, &b.RepoID, &b.IssueNumber, &b.AmountCents, &b.Currency,
		&checkoutID, &b.Status, &claimer, &b.CreatedAt,
		&mpRepoID, &mpPRNumber, &mpSHA, &mpAuthor, &mpMergedAt, &mpBody,
		&aiScore, &aiSummary,
		&payTransferID, &payCreatedAt,
	)
	if err != nil {
		return nil, err
	}
	b.StripeCheckoutID = checkoutID.String
	b.ClaimerGitHubLogin = claimer.String

	if mpRepoID.Valid {
		b.MergedPR = &store.MergedPR{
			RepoID:   mpRepoID.String,
			PRNumber: int(mpPRNumber.Int32),
			SHA:      mpSHA.String,
			Author:   mpAuthor.String,
			MergedAt: mpMergedAt.Time,
			Body:     mpBody.String,
		}
	}
	if aiScore.Valid {
		b.AI = &store.AIResult{
			Score:   int(aiScore.Int32),
			Summary: aiSummary.String,
		}
	}
	if payTransferID.Valid {
		b.Payout = &store.Payout{
			StripeTransferID: payTransferID.String,
			CreatedAt:        payCreatedAt.Time,
		}
	}
	return &b, nil
}

const bountySelectSQL = `
SELECT
    b.id, b.repo_id, b.issue_number, b.amount_cents, b.currency,
    b.stripe_checkout_id, b.status, b.claimer_github_login, b.created_at,
    m.repo_id,    m.pr_number, m.sha, m.author, m.merged_at, m.body,
    a.score,      a.summary,
    p.stripe_transfer_id, p.created_at
FROM bounties b
LEFT JOIN merged_prs m ON m.bounty_id = b.id
LEFT JOIN ai_results  a ON a.bounty_id = b.id
LEFT JOIN payouts     p ON p.bounty_id = b.id`

// ─── store.Store implementation ──────────────────────────────────────────────

func (p *Postgres) CreateBounty(b *store.Bounty) {
	b.ID = newID("bty")
	_, _ = p.db.Exec(
		`INSERT INTO bounties (id, repo_id, issue_number, amount_cents, currency, status, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		b.ID, b.RepoID, b.IssueNumber, b.AmountCents, b.Currency,
		string(b.Status), b.CreatedAt,
	)
}

func (p *Postgres) AttachStripeCheckout(bountyID, checkoutID string) error {
	res, err := p.db.Exec(
		`UPDATE bounties SET stripe_checkout_id = $1 WHERE id = $2`,
		checkoutID, bountyID,
	)
	if err != nil {
		return err
	}
	return expectOne(res)
}

func (p *Postgres) SetBountyStatus(bountyID string, st store.BountyStatus) error {
	res, err := p.db.Exec(
		`UPDATE bounties SET status = $1 WHERE id = $2`,
		string(st), bountyID,
	)
	if err != nil {
		return err
	}
	return expectOne(res)
}

func (p *Postgres) FindBountyByRepoIssue(repoID string, issueNumber int) (*store.Bounty, bool) {
	row := p.db.QueryRow(
		bountySelectSQL+` WHERE b.repo_id = $1 AND b.issue_number = $2`,
		repoID, issueNumber,
	)
	b, err := scanBounty(row)
	if err != nil {
		return nil, false
	}
	return b, true
}

func (p *Postgres) GetBounty(bountyID string) (*store.Bounty, bool) {
	row := p.db.QueryRow(bountySelectSQL+` WHERE b.id = $1`, bountyID)
	b, err := scanBounty(row)
	if err != nil {
		return nil, false
	}
	return b, true
}

func (p *Postgres) RecordPRMerge(bountyID string, pr store.MergedPR) error {
	_, err := p.db.Exec(
		`INSERT INTO merged_prs (bounty_id, repo_id, pr_number, sha, author, merged_at, body)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (bounty_id) DO UPDATE
		   SET repo_id = EXCLUDED.repo_id, pr_number = EXCLUDED.pr_number,
		       sha = EXCLUDED.sha, author = EXCLUDED.author,
		       merged_at = EXCLUDED.merged_at, body = EXCLUDED.body`,
		bountyID, pr.RepoID, pr.PRNumber, pr.SHA, pr.Author, pr.MergedAt, pr.Body,
	)
	return err
}

func (p *Postgres) SetAIResult(bountyID string, res store.AIResult) error {
	_, err := p.db.Exec(
		`INSERT INTO ai_results (bounty_id, score, summary)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (bounty_id) DO UPDATE SET score = EXCLUDED.score, summary = EXCLUDED.summary`,
		bountyID, res.Score, res.Summary,
	)
	return err
}

func (p *Postgres) UpsertDeveloper(d store.Developer) {
	_, _ = p.db.Exec(
		`INSERT INTO developers (github_login, stripe_account_id, stripe_onboarded, wallet_address, created_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (github_login) DO UPDATE
		   SET stripe_account_id = COALESCE(NULLIF(EXCLUDED.stripe_account_id,''), developers.stripe_account_id),
		       stripe_onboarded  = EXCLUDED.stripe_onboarded,
		       wallet_address    = COALESCE(NULLIF(EXCLUDED.wallet_address,''), developers.wallet_address)`,
		d.GitHubLogin, d.StripeAccountID, d.StripeOnboarded, d.WalletAddress, d.CreatedAt,
	)
}

func (p *Postgres) SetDeveloperWallet(login, walletAddress string) error {
	res, err := p.db.Exec(
		`UPDATE developers SET wallet_address = $1 WHERE github_login = $2`,
		walletAddress, login,
	)
	if err != nil {
		return err
	}
	return expectOne(res)
}

func (p *Postgres) SetDeveloperOnboarded(login string, onboarded bool, at time.Time) {
	_, _ = p.db.Exec(
		`UPDATE developers SET stripe_onboarded = $1, last_onboarded_at = $2 WHERE github_login = $3`,
		onboarded, at, login,
	)
}

func (p *Postgres) SetBountyClaimer(bountyID, githubLogin string) error {
	res, err := p.db.Exec(
		`UPDATE bounties SET claimer_github_login = $1
		 WHERE id = $2 AND claimer_github_login IS NULL`,
		githubLogin, bountyID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Either not found or already claimed — distinguish:
		var existing sql.NullString
		err2 := p.db.QueryRow(
			`SELECT claimer_github_login FROM bounties WHERE id = $1`, bountyID,
		).Scan(&existing)
		if err2 == sql.ErrNoRows {
			return store.ErrNotFound
		}
		return errors.New("already claimed")
	}
	return nil
}

func (p *Postgres) GetDeveloper(login string) (*store.Developer, bool) {
	var d store.Developer
	var acctID sql.NullString
	var lastAt sql.NullTime
	var walletAddr sql.NullString
	err := p.db.QueryRow(
		`SELECT github_login, stripe_account_id, stripe_onboarded, wallet_address, created_at, last_onboarded_at
		 FROM developers WHERE github_login = $1`, login,
	).Scan(&d.GitHubLogin, &acctID, &d.StripeOnboarded, &walletAddr, &d.CreatedAt, &lastAt)
	if err != nil {
		return nil, false
	}
	d.StripeAccountID = acctID.String
	d.WalletAddress = walletAddr.String
	if lastAt.Valid {
		d.LastOnboardedAt = &lastAt.Time
	}
	return &d, true
}

func (p *Postgres) AllDevelopers() []*store.Developer {
	rows, err := p.db.Query(
		`SELECT github_login, stripe_account_id, stripe_onboarded, wallet_address, created_at, last_onboarded_at FROM developers`,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*store.Developer
	for rows.Next() {
		var d store.Developer
		var acctID sql.NullString
		var walletAddr sql.NullString
		var lastAt sql.NullTime
		if err := rows.Scan(&d.GitHubLogin, &acctID, &d.StripeOnboarded, &walletAddr, &d.CreatedAt, &lastAt); err != nil {
			continue
		}
		d.StripeAccountID = acctID.String
		d.WalletAddress = walletAddr.String
		if lastAt.Valid {
			d.LastOnboardedAt = &lastAt.Time
		}
		out = append(out, &d)
	}
	return out
}

func (p *Postgres) RecordPayout(bountyID string, pay store.Payout) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		`INSERT INTO payouts (bounty_id, stripe_transfer_id, created_at)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (bounty_id) DO NOTHING`,
		bountyID, pay.StripeTransferID, pay.CreatedAt,
	)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	_, err = tx.Exec(
		`UPDATE bounties SET status = 'paid' WHERE id = $1`, bountyID,
	)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (p *Postgres) PendingPayouts() []*store.Bounty {
	rows, err := p.db.Query(
		bountySelectSQL + `
		WHERE b.status = 'funded'
		  AND m.bounty_id IS NOT NULL
		  AND p.bounty_id IS NULL`,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*store.Bounty
	for rows.Next() {
		b, err := scanBountyRow(rows)
		if err != nil {
			continue
		}
		out = append(out, b)
	}
	return out
}

// scanBountyRow scans from *sql.Rows (for multi-row queries).
func scanBountyRow(rows *sql.Rows) (*store.Bounty, error) {
	var b store.Bounty
	var checkoutID, claimer sql.NullString
	var mpRepoID, mpSHA, mpAuthor, mpBody sql.NullString
	var mpPRNumber sql.NullInt32
	var mpMergedAt sql.NullTime
	var aiScore sql.NullInt32
	var aiSummary sql.NullString
	var payTransferID sql.NullString
	var payCreatedAt sql.NullTime

	err := rows.Scan(
		&b.ID, &b.RepoID, &b.IssueNumber, &b.AmountCents, &b.Currency,
		&checkoutID, &b.Status, &claimer, &b.CreatedAt,
		&mpRepoID, &mpPRNumber, &mpSHA, &mpAuthor, &mpMergedAt, &mpBody,
		&aiScore, &aiSummary,
		&payTransferID, &payCreatedAt,
	)
	if err != nil {
		return nil, err
	}
	b.StripeCheckoutID = checkoutID.String
	b.ClaimerGitHubLogin = claimer.String
	if mpRepoID.Valid {
		b.MergedPR = &store.MergedPR{
			RepoID:   mpRepoID.String,
			PRNumber: int(mpPRNumber.Int32),
			SHA:      mpSHA.String,
			Author:   mpAuthor.String,
			MergedAt: mpMergedAt.Time,
			Body:     mpBody.String,
		}
	}
	if aiScore.Valid {
		b.AI = &store.AIResult{Score: int(aiScore.Int32), Summary: aiSummary.String}
	}
	if payTransferID.Valid {
		b.Payout = &store.Payout{StripeTransferID: payTransferID.String, CreatedAt: payCreatedAt.Time}
	}
	return &b, nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func expectOne(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func newID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}
