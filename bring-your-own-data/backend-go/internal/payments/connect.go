package payments

import (
	"errors"
	"os"

	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/account"
	"github.com/stripe/stripe-go/v76/accountlink"
	"github.com/stripe/stripe-go/v76/transfer"
)

func (c *StripeClient) CreateExpressAccount() (string, error) {
	params := &stripe.AccountParams{
		Type: stripe.String(string(stripe.AccountTypeExpress)),
	}
	acct, err := account.New(params)
	if err != nil {
		return "", err
	}
	return acct.ID, nil
}

type AccountLinkParams struct {
	AccountID  string
	RefreshURL string
	ReturnURL  string
}

func (c *StripeClient) CreateAccountLink(p AccountLinkParams) (string, error) {
	params := &stripe.AccountLinkParams{
		Account:    stripe.String(p.AccountID),
		RefreshURL: stripe.String(p.RefreshURL),
		ReturnURL:  stripe.String(p.ReturnURL),
		Type:       stripe.String(string(stripe.AccountLinkTypeAccountOnboarding)),
	}
	link, err := accountlink.New(params)
	if err != nil {
		return "", err
	}
	return link.URL, nil
}

type TransferParams struct {
	DestinationAccountID string
	AmountCents          int64
	Currency             string
	BountyID             string
}

func (c *StripeClient) CreateTransfer(p TransferParams) (string, error) {
	if p.DestinationAccountID == "" {
		return "", errors.New("destination account id is required")
	}
	if p.AmountCents <= 0 {
		return "", errors.New("amount must be positive")
	}
	params := &stripe.TransferParams{
		Amount:      stripe.Int64(p.AmountCents),
		Currency:    stripe.String(p.Currency),
		Destination: stripe.String(p.DestinationAccountID),
		Metadata: map[string]string{
			"bounty_id": p.BountyID,
		},
	}
	params.SetIdempotencyKey("bounty_transfer_" + p.BountyID)

	t, err := transfer.New(params)
	if err != nil {
		return "", err
	}
	return t.ID, nil
}

func ConnectEnabled() bool {
	return os.Getenv("STRIPE_SECRET_KEY") != "" && os.Getenv("STRIPE_WEBHOOK_SECRET") != ""
}
