package checkout

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

const fakeProductBody = `{"data":{"web_subscription_product_details_by_rest_id":{"rest_id":"prod_TJXL297k753Zvb","product_name":"Premium+ Gift","display":{"title":"Premium+","badge_text":"x","marketing_label":"Best value"},"features":[{"description":"No ads"}],"prices":[{"amount_local_micro":12340000,"currency_code":"BDT","price_type":"OneTime"}]}}}`

type fakeX struct {
	mu       sync.Mutex
	requests []*http.Request
	status   int
	body     string
}

// newFakeXClient returns an xClient whose direct and regional transports both
// go to an httptest server, recording every request that would hit x.com.
func newFakeXClient(t *testing.T, fx *fakeX) *xClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fx.mu.Lock()
		fx.requests = append(fx.requests, r.Clone(context.Background()))
		status, body := fx.status, fx.body
		fx.mu.Unlock()
		if status == 0 {
			status = 200
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	rt := stripeRoundTrip(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		return http.DefaultTransport.RoundTrip(r)
	})
	client := &http.Client{Transport: rt}
	return &xClient{vault: controlFixture(t), http: client, regionalHTTP: client, headers: http.Header{"Cookie": {"auth_token=secret"}}}
}

func TestQuoteParsesPricesAndNameFields(t *testing.T) {
	fx := &fakeX{body: fakeProductBody}
	c := newFakeXClient(t, fx)
	quotes, err := c.readOnlyQuotes(context.Background(), []string{"prod_TJXL297k753Zvb"})
	if err != nil {
		t.Fatal(err)
	}
	q := quotes[0]
	if q.RestID != "prod_TJXL297k753Zvb" || len(q.Prices) != 1 {
		t.Fatalf("unexpected quote %+v", q)
	}
	if m, ok := q.Prices[0].Minor(); !ok || m != 1234 || q.Prices[0].CurrencyCode != "BDT" || q.Prices[0].PriceType != "OneTime" {
		t.Fatalf("unexpected price %+v", q.Prices[0])
	}
	got := map[string]string{}
	for _, f := range q.TextFields {
		got[f.Path] = f.Value
	}
	for path, want := range map[string]string{"product_name": "Premium+ Gift", "display.title": "Premium+", "display.marketing_label": "Best value", "features[0].description": "No ads"} {
		if got[path] != want {
			t.Fatalf("field %s = %q, want %q (all: %v)", path, got[path], want, got)
		}
	}
	if _, ok := got["display.badge_text"]; ok {
		t.Fatal("non name-like field extracted")
	}
	if name, ok := q.ProductName(); !ok || name != "Premium+ Gift" {
		t.Fatalf("ProductName = %q %v", name, ok)
	}
	if len(fx.requests) != 1 {
		t.Fatalf("requests = %d", len(fx.requests))
	}
	r := fx.requests[0]
	if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/Se1Bp6zcNnuXYXRecV2qLA/useSubscriptionProductDetailsByRestIdQuery") {
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
	}
	if !strings.Contains(r.URL.Query().Get("variables"), `"stripeId":"prod_TJXL297k753Zvb"`) || r.URL.Query().Get("features") == "" {
		t.Fatalf("unexpected query %s", r.URL.RawQuery)
	}
}

func TestQuoteNameOnlyFromTopLevel(t *testing.T) {
	q, err := ParseProductDetails([]byte(`{"data":{"web_subscription_product_details_by_rest_id":{"rest_id":"prod_A1","display":{"name":"Nested"},"prices":[]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := q.ProductName(); ok {
		t.Fatal("nested name must not be treated as the product name")
	}
	if len(q.TextFields) != 1 || q.TextFields[0].Path != "display.name" {
		t.Fatalf("fields %+v", q.TextFields)
	}
	if _, err = ParseProductDetails([]byte(`{"data":{"web_subscription_product_details_by_rest_id":null}}`)); !errors.Is(err, ErrProductMissing) {
		t.Fatalf("err = %v", err)
	}
}

func TestQuoteRejectsInvalidProductIDsWithoutRequests(t *testing.T) {
	fx := &fakeX{body: fakeProductBody}
	c := newFakeXClient(t, fx)
	for _, id := range []string{"", "prod_", "price_123", "prod_abc/../x", "prod_abc&x=1", "PROD_abc"} {
		if _, err := c.readOnlyQuotes(context.Background(), []string{id}); err == nil {
			t.Fatalf("accepted %q", id)
		}
		if _, err := QuoteProducts(context.Background(), nil, 1, []string{id}); err == nil {
			t.Fatalf("QuoteProducts accepted %q", id)
		}
	}
	if len(fx.requests) != 0 {
		t.Fatalf("invalid IDs sent %d requests", len(fx.requests))
	}
}

func TestQuoteClientNeverSendsMutations(t *testing.T) {
	fx := &fakeX{body: fakeProductBody}
	c := newFakeXClient(t, fx)
	if _, err := c.readOnlyQuotes(context.Background(), []string{"prod_TJXL297k753Zvb"}); err != nil {
		t.Fatal(err)
	}
	fx.requests = nil
	// After quoting, the same client must refuse every other operation.
	if _, _, err := c.create(context.Background(), "someone", "123", Plan{ProductID: "prod_TJXL297k753Zvb"}); !errors.Is(err, ErrReadOnlyViolation) {
		t.Fatalf("create err = %v", err)
	}
	var out any
	if err := c.call(context.Background(), "u", "useOneTimePurchaseGiftMutation", "GqTVJ4S1526tLkxj69xIZw", map[string]string{}, true, &out); !errors.Is(err, ErrReadOnlyViolation) {
		t.Fatalf("mutation err = %v", err)
	}
	if err := c.callOnce(context.Background(), "u", productDetailsOperation, productDetailsQueryID, map[string]string{}, true, &out); !errors.Is(err, ErrReadOnlyViolation) {
		t.Fatalf("allowed name sent as mutation err = %v", err)
	}
	if _, err := c.recipient(context.Background(), "someone"); !errors.Is(err, ErrReadOnlyViolation) {
		t.Fatalf("PremiumGiftingQuery err = %v", err)
	}
	if len(fx.requests) != 0 {
		t.Fatalf("read-only client sent %d forbidden requests", len(fx.requests))
	}
	for _, prefix := range []string{"x-create-attempt:", "checkout-creation"} {
		if keys, err := c.vault.NamesWithPrefix(prefix); err != nil || len(keys) != 0 {
			t.Fatalf("%s recorded: %v %v", prefix, keys, err)
		}
	}
}

func TestQuoteHTTPErrorsAreTyped(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{{404, `{}`}, {401, `{}`}} {
		fx := &fakeX{status: tc.status, body: tc.body}
		c := newFakeXClient(t, fx)
		_, err := c.readOnlyQuotes(context.Background(), []string{"prod_TJXL297k753Zvb"})
		var se *XHTTPStatusError
		if !errors.As(err, &se) || se.Status != tc.status || !errors.Is(err, ErrXReadFailure) {
			t.Fatalf("status %d: err = %v", tc.status, err)
		}
	}
	fx := &fakeX{body: `{"errors":[{"name":"AuthorizationError","message":"not logged in"}]}`}
	c := newFakeXClient(t, fx)
	_, err := c.readOnlyQuotes(context.Background(), []string{"prod_TJXL297k753Zvb"})
	var ge *XGraphQLError
	if !errors.As(err, &ge) || len(ge.Messages) != 1 || !strings.Contains(ge.Messages[0], "AuthorizationError") {
		t.Fatalf("graphql err = %v", err)
	}
}

func TestParseCatalogRejectsQuotePlaceholderName(t *testing.T) {
	raw := `{"merchant":"acct_TESTMERCHANT","currency":"bdt","plans":[{"tier":"premium_plus","months":3,"amount":100,"product":"prod_TJXL297k753Zvb","name":"<fill from Stripe checkout>"}]}`
	if _, err := ParseCatalog([]byte(raw)); err == nil || !strings.Contains(err.Error(), "placeholder") {
		t.Fatalf("err = %v", err)
	}
}
