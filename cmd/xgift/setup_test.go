package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xgift/internal/checkout"
	"xgift/internal/vault"
)

func testWizard(input string) *wizard {
	return &wizard{r: bufio.NewReader(strings.NewReader(input))}
}

func TestSetupCatalogDefaultStaysPremiumOnly(t *testing.T) {
	dir := t.TempDir()
	password := filepath.Join(dir, "password")
	if err := os.WriteFile(password, []byte(strings.Repeat("p", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(filepath.Join(dir, "vault.db"), password, true)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	// Accept the default catalog, decline Premium+.
	if err = testWizard("\n\n").setupCatalog(v); err != nil {
		t.Fatal(err)
	}
	cat, err := checkout.ReadCatalog(v)
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Plans) != 2 || cat.Merchant != defaultXMerchant || cat.Currency != defaultXCurrency {
		t.Fatalf("default catalog changed: %+v", cat)
	}
	for _, p := range cat.Plans {
		if p.Tier != checkout.TierPremium || p.Name != "" {
			t.Fatalf("default plan is not plain premium: %+v", p)
		}
	}
	// Accept the default catalog and add one Premium+ plan.
	if err = testWizard("\ny\n1\n12\n2400\nprod_PLUS12MO\n Premium+ Gift - 12 months \n").setupCatalog(v); err != nil {
		t.Fatal(err)
	}
	if cat, err = checkout.ReadCatalog(v); err != nil {
		t.Fatal(err)
	}
	plan, err := cat.PlanFor(checkout.TierPremiumPlus, 12)
	if err != nil || plan.ProductID != "prod_PLUS12MO" || plan.Minor != 240000 || plan.Name() != "Premium+ Gift - 12 months" || len(cat.Plans) != 3 {
		t.Fatalf("premium_plus plan: %+v %v", plan, err)
	}
}

func TestAddPremiumPlusPlansValidation(t *testing.T) {
	base := func() checkout.Catalog {
		return checkout.Catalog{Merchant: defaultXMerchant, Currency: defaultXCurrency, Plans: []checkout.CatalogPlan{
			{Tier: checkout.TierPremium, Months: 3, Amount: 30000, Product: defaultXProduct3Mo},
			{Tier: checkout.TierPremium, Months: 6, Amount: 60000, Product: defaultXProduct6Mo},
		}}
	}
	c := base()
	if err := testWizard("y\n3\n").addPremiumPlusPlans(&c); err == nil {
		t.Fatal("exceeded the 4-plan limit")
	}
	c = base()
	if err := testWizard("y\n1\n12\n2400\nprod_PLUS12MO\n\n").addPremiumPlusPlans(&c); err == nil {
		t.Fatal("accepted Premium+ plan without Stripe product name")
	}
	c = base()
	if err := testWizard("y\n2\n6\n1200\nprod_PLUS6MO\nPremium+ Gift - 6 months\n12\n2400\nprod_PLUS12MO\nPremium+ Gift - 12 months\n").addPremiumPlusPlans(&c); err != nil || len(c.Plans) != 4 {
		t.Fatalf("two Premium+ plans: %v %+v", err, c.Plans)
	}
	full := c
	if err := testWizard("").addPremiumPlusPlans(&full); err != nil || len(full.Plans) != 4 {
		t.Fatal("full catalog should skip the Premium+ prompt")
	}
}
