package checkout

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const legacyCatalog = `{"merchant":"acct_TESTMERCHANT","currency":"usd","plans":[{"months":3,"amount":30000,"product":"prod_TEST3MO"},{"months":6,"amount":60000,"product":"prod_TEST6MO"}]}`

const mixedCatalog = `{"merchant":"acct_TESTMERCHANT","currency":"usd","plans":[
 {"tier":"premium","months":3,"amount":30000,"product":"prod_TEST3MO"},
 {"tier":"premium","months":6,"amount":60000,"product":"prod_TEST6MO"},
 {"tier":"premium_plus","months":6,"amount":120000,"product":"prod_PLUS6MO","name":"Premium+ Gift - 6 months"},
 {"tier":"premium_plus","months":12,"amount":240000,"product":"prod_PLUS12MO","name":"Premium+ Gift - 12 months"}]}`

func TestParseCatalogDefaultsMissingTierToPremium(t *testing.T) {
	c, err := ParseCatalog([]byte(legacyCatalog))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range c.Plans {
		if p.Tier != TierPremium {
			t.Fatalf("legacy plan tier = %q, want premium", p.Tier)
		}
	}
	plan, err := c.PlanFor("", 6)
	if err != nil || plan.Tier != TierPremium || plan.ProductID != "prod_TEST6MO" || plan.Name() != "Premium Gift - 6 months" {
		t.Fatalf("legacy plan resolution: %+v %v", plan, err)
	}
	if _, err = c.PlanFor(TierPremiumPlus, 6); err == nil {
		t.Fatal("premium_plus resolved from a premium-only catalog")
	}
}

func TestParseCatalogRejectsInvalidPlans(t *testing.T) {
	plan := func(tier string, months int, product, name string) string {
		b, _ := json.Marshal(map[string]any{"tier": tier, "months": months, "amount": 1000, "product": product, "name": name})
		return string(b)
	}
	catalog := func(plans ...string) []byte {
		return []byte(`{"merchant":"acct_T","currency":"usd","plans":[` + strings.Join(plans, ",") + `]}`)
	}
	for _, tc := range []struct {
		name string
		raw  []byte
		ok   bool
	}{
		{"same months across tiers", catalog(plan("premium", 6, "prod_A", ""), plan("premium_plus", 6, "prod_B", "Premium+ Gift - 6 months")), true},
		{"duplicate tier and months", catalog(plan("premium", 6, "prod_A", ""), plan("premium", 6, "prod_B", "")), false},
		{"duplicate implicit premium", []byte(`{"merchant":"acct_T","currency":"usd","plans":[{"months":6,"amount":1,"product":"prod_A"},{"tier":"premium","months":6,"amount":1,"product":"prod_B"}]}`), false},
		{"duplicate premium_plus", catalog(plan("premium_plus", 12, "prod_A", "X"), plan("premium_plus", 12, "prod_B", "Y")), false},
		{"four plans", catalog(plan("premium", 3, "prod_A", ""), plan("premium", 6, "prod_B", ""), plan("premium_plus", 3, "prod_C", "C"), plan("premium_plus", 12, "prod_D", "D")), true},
		{"five plans", catalog(plan("premium", 3, "prod_A", ""), plan("premium", 6, "prod_B", ""), plan("premium_plus", 3, "prod_C", "C"), plan("premium_plus", 12, "prod_D", "D"), plan("premium", 1, "prod_E", "")), false},
		{"no plans", catalog(), false},
		{"unknown tier", catalog(plan("premium_pro", 6, "prod_A", "X")), false},
		{"tier with spaces", catalog(plan(" premium", 6, "prod_A", "")), false},
		{"premium_plus without name", catalog(plan("premium_plus", 12, "prod_A", "")), false},
		{"premium with explicit name", catalog(plan("premium", 6, "prod_A", "Premium Gift - 6 months")), true},
		{"control character in name", catalog(plan("premium_plus", 12, "prod_A", "Premium+\nGift")), false},
		{"surrounding spaces in name", catalog(plan("premium_plus", 12, "prod_A", " Premium+ Gift ")), false},
		{"overlong name", catalog(plan("premium_plus", 12, "prod_A", strings.Repeat("P", MaxPlanNameBytes+1))), false},
		{"max length name", catalog(plan("premium_plus", 12, "prod_A", strings.Repeat("P", MaxPlanNameBytes))), true},
		{"reused product", catalog(plan("premium", 6, "prod_A", ""), plan("premium_plus", 12, "prod_A", "X")), false},
		{"months out of range", catalog(plan("premium_plus", 25, "prod_A", "X")), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCatalog(tc.raw)
			if tc.ok && err != nil {
				t.Fatalf("rejected valid catalog: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("accepted invalid catalog")
			}
			if err != nil && strings.Contains(err.Error(), "prod_") {
				t.Fatal("catalog values leaked into error text")
			}
		})
	}
}

func TestPlanResolutionByTierAndMonths(t *testing.T) {
	c, err := ParseCatalog([]byte(mixedCatalog))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		tier          Tier
		months        int
		product, name string
		label         string
		minor         int
	}{
		{TierPremium, 3, "prod_TEST3MO", "Premium Gift - 3 months", "Premium", 30000},
		{"", 6, "prod_TEST6MO", "Premium Gift - 6 months", "Premium", 60000},
		{TierPremiumPlus, 6, "prod_PLUS6MO", "Premium+ Gift - 6 months", "Premium+", 120000},
		{TierPremiumPlus, 12, "prod_PLUS12MO", "Premium+ Gift - 12 months", "Premium+", 240000},
	} {
		p, err := c.PlanFor(tc.tier, tc.months)
		if err != nil {
			t.Fatal(err)
		}
		if p.ProductID != tc.product || p.Name() != tc.name || p.Label() != tc.label || p.Minor != tc.minor || p.Merchant != "acct_TESTMERCHANT" || p.Currency != "usd" || p.Tier != tc.tier.Normalize() {
			t.Fatalf("%s/%d resolved to %+v", tc.tier, tc.months, p)
		}
	}
	for _, miss := range []struct {
		tier   Tier
		months int
	}{{TierPremium, 12}, {TierPremiumPlus, 3}, {"premium_pro", 6}} {
		if _, err := c.PlanFor(miss.tier, miss.months); err == nil {
			t.Fatalf("%s/%d should not resolve", miss.tier, miss.months)
		}
	}
}

func TestPlanNameAndLabels(t *testing.T) {
	if got := (Plan{Months: 3}).Name(); got != "Premium Gift - 3 months" {
		t.Fatal(got)
	}
	if got := (Plan{Tier: TierPremiumPlus, Months: 12}).Name(); got != "Premium+ Gift - 12 months" {
		t.Fatal(got)
	}
	if got := (Plan{Tier: TierPremiumPlus, Months: 12, ProductName: "X Premium+ (1 year gift)"}).Name(); got != "X Premium+ (1 year gift)" {
		t.Fatal(got)
	}
	if TierLabel("") != "Premium" || TierLabel("premium") != "Premium" || TierLabel("premium_plus") != "Premium+" {
		t.Fatal("tier labels")
	}
	if got := GiftDescription(TierPremiumPlus, 12); got != "12 个月 Premium+" {
		t.Fatal(got)
	}
}

func TestLegacyRecordsAreBoundToPremium(t *testing.T) {
	var r Record
	if err := json.Unmarshal([]byte(`{"username":"recipient","recipient_id":"1234","months":6,"amount_minor":60000,"currency":"USD","product_id":"prod_TEST6MO","status":"created"}`), &r); err != nil {
		t.Fatal(err)
	}
	c, _ := ParseCatalog([]byte(mixedCatalog))
	premium, _ := c.PlanFor(TierPremium, 6)
	plus, _ := c.PlanFor(TierPremiumPlus, 6)
	if r.PlanTier() != TierPremium || !r.matchesPlan(premium) || r.matchesPlan(plus) {
		t.Fatal("legacy record not bound to premium")
	}
	// A Premium+ order with the same duration must not satisfy a Premium plan,
	// even if someone rewrote its product to match.
	r.Tier = TierPremiumPlus
	if r.matchesPlan(premium) {
		t.Fatal("tier mismatch ignored")
	}
	r.ProductID, r.Amount = plus.ProductID, plus.Minor
	if !r.matchesPlan(plus) {
		t.Fatal("premium_plus record does not match its plan")
	}
	got := c.PlanForRecord(&r)
	if got.Name() != "Premium+ Gift - 6 months" || got.Tier != TierPremiumPlus || got.Merchant != c.Merchant {
		t.Fatalf("record plan: %+v", got)
	}
	// New records serialise their tier; legacy JSON keeps working.
	b, _ := json.Marshal(Record{Tier: TierPremiumPlus})
	if !strings.Contains(string(b), `"tier":"premium_plus"`) {
		t.Fatal(string(b))
	}
}

func TestStripeGuardRequiresConfiguredPremiumPlusName(t *testing.T) {
	c, _ := ParseCatalog([]byte(mixedCatalog))
	plan, _ := c.PlanFor(TierPremiumPlus, 12)
	r := Record{Username: "recipient", RecipientID: "1234", SessionID: "cs_live_Test"}
	if err := publicPageFixture(&r, plan).guard(&r, plan, true); err != nil {
		t.Fatalf("configured Premium+ name rejected: %v", err)
	}
	// Stripe showing a different product name (e.g. a guessed default) must block payment.
	wrong := plan
	wrong.ProductName = "Premium Gift - 12 months"
	if err := publicPageFixture(&r, wrong).guard(&r, plan, true); err == nil {
		t.Fatal("mismatched Stripe product name accepted")
	}
}

func TestPublicLinkTierSwitchNeedsNewOrder(t *testing.T) {
	c, _ := ParseCatalog([]byte(mixedCatalog))
	premium, _ := c.PlanFor(TierPremium, 6)
	plus, _ := c.PlanFor(TierPremiumPlus, 6)
	r := Record{Username: "recipient", RecipientID: "1234", Months: 6, Amount: premium.Minor, Currency: "USD", ProductID: premium.ProductID, Status: "created", SessionID: "cs_live_TestPublic123", URL: "https://checkout.stripe.com/c/pay/cs_live_TestPublic123"}
	if !publicLinkMatches(&r, premium) || publicLinkMatches(&r, plus) {
		t.Fatal("cached public link reused across tiers")
	}
	v := controlFixture(t)
	r.Tier = "premium_pro"
	b, _ := json.Marshal(publicLinkRecord{Owner: "owner", Order: r})
	v.Put("public-checkout:1234", b)
	if got, err := publicLinkExisting(v, "recipient", "1234", premium); err == nil || got != nil {
		t.Fatal("unknown stored tier accepted")
	}
}

func TestResumeRejectsOrderOfAnotherTier(t *testing.T) {
	v := controlFixture(t)
	v.Put("catalog", []byte(mixedCatalog))
	r := Record{Username: "recipient", RecipientID: "1234", Tier: TierPremiumPlus, Months: 6, Amount: 120000, Currency: "USD", ProductID: "prod_PLUS6MO", SessionID: "cs_live_Original123", URL: "https://checkout.stripe.com/c/pay/cs_live_Original123", Status: "created", Created: 123}
	b, _ := json.Marshal(r)
	v.Put("checkout:1234", b)
	// No X/Stripe credentials exist: the mismatch must be detected before any upstream call.
	if _, err := ResumeForRecipient(context.Background(), v, "recipient", "1234", 0, TierPremium, 6); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("premium code resumed a premium_plus order: %v", err)
	}
}
