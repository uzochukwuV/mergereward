package payments

import (
	"errors"
	"fmt"
	"os"

	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/checkout/session"
)

type StripeClient struct {
	WebhookSecret string
}

type CreateCheckoutParams struct {
	BountyID    string
	RepoID      string
	IssueNumber int
	AmountCents int64
	Currency    string
	SuccessURL  string
	CancelURL   string
}

func NewStripeClientFromEnv() (*StripeClient, error) {
	key := os.Getenv("STRIPE_SECRET_KEY")
	if key == "" {
		return nil, errors.New("STRIPE_SECRET_KEY is required")
	}
	stripe.Key = key

	wh := os.Getenv("STRIPE_WEBHOOK_SECRET")
	if wh == "" {
		return nil, errors.New("STRIPE_WEBHOOK_SECRET is required")
	}
	return &StripeClient{WebhookSecret: wh}, nil
}

type CheckoutSession struct {
	ID  string
	URL string
}

func (c *StripeClient) CreateBountyCheckoutSession(p CreateCheckoutParams) (*CheckoutSession, error) {
	params := &stripe.CheckoutSessionParams{
		Mode:       stripe.String(string(stripe.CheckoutSessionModePayment)),
		SuccessURL: stripe.String(p.SuccessURL),
		CancelURL:  stripe.String(p.CancelURL),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				Quantity: stripe.Int64(1),
				PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
					Currency: stripe.String(p.Currency),
					UnitAmount: stripe.Int64(p.AmountCents),
					ProductData: &stripe.CheckoutSessionLineItemPriceDataProductDataParams{
						Name: stripe.String(fmt.Sprintf("Bounty for %s#%d", p.RepoID, p.IssueNumber)),
					},
				},
			},
		},
		Metadata: map[string]string{
			"bounty_id":   p.BountyID,
			"repo_id":     p.RepoID,
			"issue_number": fmt.Sprintf("%d", p.IssueNumber),
		},
	}

	s, err := session.New(params)
	if err != nil {
		return nil, err
	}
	return &CheckoutSession{ID: s.ID, URL: s.URL}, nil
}
