package checkout

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
	"xgift/internal/vault"
)

// Tier is the X subscription level a gift product grants. X decides the tier
// solely from the Stripe product ID (external_product_id); this field only
// lets the operator label, select and cross-check plans.
type Tier string

const (
	TierPremium     Tier = "premium"
	TierPremiumPlus Tier = "premium_plus"
)

// ParseTier accepts the stored/JSON spelling. An empty value means Premium so
// records written before Premium+ support keep their original meaning.
func ParseTier(s string) (Tier, bool) {
	switch Tier(s) {
	case "", TierPremium:
		return TierPremium, true
	case TierPremiumPlus:
		return TierPremiumPlus, true
	}
	return "", false
}

// Normalize maps the legacy empty tier to Premium; unknown values stay as-is
// so comparisons with a valid tier fail closed.
func (t Tier) Normalize() Tier {
	if t == "" {
		return TierPremium
	}
	return t
}

// Label is the user-facing product name, e.g. in Chinese status messages.
func (t Tier) Label() string {
	if t.Normalize() == TierPremiumPlus {
		return "Premium+"
	}
	return "Premium"
}

// TierLabel returns the display label for a stored tier string.
func TierLabel(s string) string { return Tier(s).Label() }

// GiftDescription is the Chinese description of a gift, e.g. "6 个月 Premium+".
func GiftDescription(t Tier, months int) string {
	return fmt.Sprintf("%d 个月 %s", months, t.Label())
}

// Catalog is the operator-configured Stripe merchant and gift plan list,
// stored as the encrypted vault record "catalog". Amounts are minor units.
type Catalog struct {
	Merchant string        `json:"merchant"`
	Currency string        `json:"currency"`
	Plans    []CatalogPlan `json:"plans"`
}

type CatalogPlan struct {
	// Tier defaults to premium when omitted (catalogs written before
	// Premium+ support).
	Tier    Tier   `json:"tier,omitempty"`
	Months  int    `json:"months"`
	Amount  int    `json:"amount"`
	Product string `json:"product"`
	// Name is the exact Stripe product / line-item name shown at checkout.
	// The payment guard refuses to pay unless Stripe reports exactly this
	// name. Optional for premium (defaults to "Premium Gift - N months");
	// required for premium_plus because X's naming is not known in advance.
	Name string `json:"name,omitempty"`
}

// MaxCatalogPlans allows e.g. Premium 3/6 months plus two Premium+ plans.
const MaxCatalogPlans = 4

// MaxPlanNameBytes bounds the configured Stripe product name.
const MaxPlanNameBytes = 120

var (
	catalogMerchantPattern = regexp.MustCompile(`^acct_[A-Za-z0-9]+$`)
	catalogCurrencyPattern = regexp.MustCompile(`^[a-z]{3}$`)
	catalogProductPattern  = regexp.MustCompile(`^prod_[A-Za-z0-9]+$`)
)

// ValidPlanName reports whether name is acceptable as an exact Stripe product
// name: non-empty, bounded, valid UTF-8, no control characters and no
// surrounding whitespace (which would silently never match Stripe).
func ValidPlanName(name string) bool {
	if name == "" || len(name) > MaxPlanNameBytes || !utf8.ValidString(name) || strings.TrimSpace(name) != name {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return false
		}
	}
	return true
}

type planKey struct {
	tier   Tier
	months int
}

// ParseCatalog validates catalog JSON. Values never appear in error text.
func ParseCatalog(raw []byte) (Catalog, error) {
	var c Catalog
	if json.Unmarshal(raw, &c) != nil {
		return c, errors.New("invalid catalog JSON")
	}
	if !catalogMerchantPattern.MatchString(c.Merchant) {
		return c, errors.New("invalid catalog merchant")
	}
	if !catalogCurrencyPattern.MatchString(c.Currency) {
		return c, errors.New("invalid catalog currency")
	}
	if len(c.Plans) < 1 || len(c.Plans) > MaxCatalogPlans {
		return c, fmt.Errorf("catalog must define 1 to %d plans", MaxCatalogPlans)
	}
	seen := map[planKey]bool{}
	products := map[string]bool{}
	for i := range c.Plans {
		p := &c.Plans[i]
		tier, ok := ParseTier(string(p.Tier))
		if !ok {
			return c, errors.New("catalog plan tier must be premium or premium_plus")
		}
		p.Tier = tier
		key := planKey{tier, p.Months}
		if p.Months < 1 || p.Months > 24 || seen[key] {
			return c, errors.New("catalog plan months must be values from 1 to 24, unique per tier")
		}
		seen[key] = true
		if p.Amount <= 0 {
			return c, errors.New("catalog plan amount must be positive")
		}
		if !catalogProductPattern.MatchString(p.Product) {
			return c, errors.New("invalid catalog plan product")
		}
		// X derives tier and duration from the product, so one product can
		// never serve two plans.
		if products[p.Product] {
			return c, errors.New("catalog plan products must be unique")
		}
		products[p.Product] = true
		if tier == TierPremiumPlus && p.Name == "" {
			return c, errors.New("premium_plus catalog plans must set name to the exact Stripe product name")
		}
		if p.Name != "" && !ValidPlanName(p.Name) {
			return c, fmt.Errorf("catalog plan name must be 1-%d bytes without control characters or surrounding spaces", MaxPlanNameBytes)
		}
	}
	return c, nil
}

// ReadCatalog loads the catalog per checkout flow, like the Stripe key, so the
// site boots before the record exists and checkouts fail with a clear error.
func ReadCatalog(v *vault.Vault) (Catalog, error) {
	raw, err := v.Get("catalog")
	if err != nil {
		return Catalog{}, errors.New("catalog record is missing or unreadable; write it with setup or put --name catalog")
	}
	defer clear(raw)
	return ParseCatalog(raw)
}

func (c Catalog) plan(p CatalogPlan) Plan {
	return Plan{Tier: p.Tier.Normalize(), Months: p.Months, Minor: p.Amount, ProductID: p.Product, ProductName: p.Name, Merchant: c.Merchant, Currency: c.Currency}
}

// PlanFor resolves a configured plan by tier and duration; unknown
// combinations are rejected. An empty tier means Premium.
func (c Catalog) PlanFor(tier Tier, months int) (Plan, error) {
	tier = tier.Normalize()
	for _, p := range c.Plans {
		if p.Tier.Normalize() == tier && p.Months == months {
			return c.plan(p), nil
		}
	}
	return Plan{}, errors.New("no catalog plan allows this tier and duration")
}

// PlanForRecord rebuilds the plan an existing order was created for from the
// order's own price evidence. The catalog contributes only the merchant and,
// when the same product is still configured, its exact Stripe product name.
func (c Catalog) PlanForRecord(r *Record) Plan {
	p := Plan{Tier: r.PlanTier(), Months: r.Months, Minor: r.Amount, Currency: strings.ToLower(r.Currency), ProductID: r.ProductID, Merchant: c.Merchant}
	for _, configured := range c.Plans {
		if configured.Product == r.ProductID && configured.Tier.Normalize() == p.Tier && configured.Months == r.Months {
			p.ProductName = configured.Name
			break
		}
	}
	return p
}
