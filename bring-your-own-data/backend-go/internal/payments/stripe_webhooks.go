package payments

import (
	"encoding/json"

	"github.com/stripe/stripe-go/v76"
)

type WebhookResult struct {
	Handled bool

	BountyID     string
	BountyStatus string

	ConnectedAccountID string
	AccountOnboarded   *bool
}

func (c *StripeClient) HandleEvent(e stripe.Event) WebhookResult {
	switch e.Type {
	case "checkout.session.completed":
		var s stripe.CheckoutSession
		if err := json.Unmarshal(e.Data.Raw, &s); err != nil {
			return WebhookResult{}
		}
		bountyID := ""
		if s.Metadata != nil {
			bountyID = s.Metadata["bounty_id"]
		}
		return WebhookResult{Handled: true, BountyID: bountyID, BountyStatus: "funded"}
	case "account.updated":
		var a stripe.Account
		if err := json.Unmarshal(e.Data.Raw, &a); err != nil {
			return WebhookResult{}
		}
		onboarded := a.ChargesEnabled && a.PayoutsEnabled
		return WebhookResult{Handled: true, ConnectedAccountID: a.ID, AccountOnboarded: &onboarded}
	default:
		return WebhookResult{}
	}
}
