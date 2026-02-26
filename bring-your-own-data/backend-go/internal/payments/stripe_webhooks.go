package payments

import (
	"encoding/json"

	"github.com/stripe/stripe-go/v76"
)

// HandleEvent returns (bountyID, status, handled).
// status is a store-style string like: "funded".
func (c *StripeClient) HandleEvent(e stripe.Event) (string, string, bool) {
	switch e.Type {
	case "checkout.session.completed":
		var s stripe.CheckoutSession
		if err := json.Unmarshal(e.Data.Raw, &s); err != nil {
			return "", "", false
		}
		bountyID := ""
		if s.Metadata != nil {
			bountyID = s.Metadata["bounty_id"]
		}
		return bountyID, "funded", true
	default:
		return "", "", false
	}
}
