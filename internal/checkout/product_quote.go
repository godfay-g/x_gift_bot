package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"xgift/internal/vault"
)

// The only X operation `xgift quote` may send. It is a GET (non-mutation)
// GraphQL query that returns the regional price list for a Stripe product.
const (
	productDetailsOperation = "useSubscriptionProductDetailsByRestIdQuery"
	productDetailsQueryID   = "Se1Bp6zcNnuXYXRecV2qLA"
)

// ValidProductID reports whether id looks like a Stripe product ID (prod_...),
// using the same pattern as the catalog validator.
func ValidProductID(id string) bool { return catalogProductPattern.MatchString(id) }

// ProductPrice is one entry of web_subscription_product_details_by_rest_id.prices.
type ProductPrice struct {
	AmountLocalMicro int64  `json:"amount_local_micro"`
	CurrencyCode     string `json:"currency_code"`
	PriceType        string `json:"price_type"`
}

// Minor returns the amount in minor currency units (micro / 10000) and whether
// the micro amount is exactly divisible, i.e. usable as a catalog amount.
func (p ProductPrice) Minor() (int64, bool) {
	if p.AmountLocalMicro <= 0 || p.AmountLocalMicro%10000 != 0 {
		return 0, false
	}
	return p.AmountLocalMicro / 10000, true
}

// ProductTextField is a string field found anywhere under the product object
// whose key looks like a name, title, description or label.
type ProductTextField struct {
	Path  string
	Value string
}

// ProductQuote is the read-only result of a product price lookup.
type ProductQuote struct {
	RestID string
	Prices []ProductPrice
	// TextFields lists name/title/description/label-like string fields.
	TextFields []ProductTextField
	// Raw is the full response body (contains no request headers or cookies).
	Raw json.RawMessage
}

// QuoteProducts reads X's regional gift price for each product through the
// configured regional proxy on port. It only ever sends the read-only
// product-details query; checkout creation, eligibility queries and Stripe are
// never touched. The returned slice has one entry per successful product; on
// the first failure the error names the failing product.
func QuoteProducts(ctx context.Context, v *vault.Vault, port int, products []string) ([]ProductQuote, error) {
	for _, id := range products {
		if !ValidProductID(id) {
			return nil, fmt.Errorf("invalid product ID %q: must match prod_[A-Za-z0-9]+", id)
		}
	}
	c, err := newXClient(v, port)
	if err != nil {
		return nil, err
	}
	defer c.close()
	return c.readOnlyQuotes(ctx, products)
}

// readOnlyQuotes switches the client into read-only mode before any request.
func (c *xClient) readOnlyQuotes(ctx context.Context, products []string) ([]ProductQuote, error) {
	c.readOnlyOps = map[string]bool{productDetailsOperation: true}
	out := make([]ProductQuote, 0, len(products))
	for _, id := range products {
		q, err := c.productDetails(ctx, id)
		if err != nil {
			return out, fmt.Errorf("%s: %w", id, err)
		}
		out = append(out, q)
	}
	return out, nil
}

func (c *xClient) productDetails(ctx context.Context, id string) (ProductQuote, error) {
	if !ValidProductID(id) {
		return ProductQuote{}, fmt.Errorf("invalid product ID %q", id)
	}
	var raw json.RawMessage
	if err := c.call(ctx, "", productDetailsOperation, productDetailsQueryID, map[string]string{"stripeId": id}, false, &raw); err != nil {
		return ProductQuote{}, err
	}
	return ParseProductDetails(raw)
}

// ErrProductMissing means X answered without a product object for the ID.
var ErrProductMissing = errors.New("X response has no web_subscription_product_details_by_rest_id product")

// ParseProductDetails decodes a useSubscriptionProductDetailsByRestIdQuery body.
func ParseProductDetails(body []byte) (ProductQuote, error) {
	q := ProductQuote{Raw: append(json.RawMessage(nil), body...)}
	var r struct {
		Data struct {
			Product json.RawMessage `json:"web_subscription_product_details_by_rest_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return q, errors.New("X returned non-JSON data")
	}
	if len(r.Data.Product) == 0 || string(r.Data.Product) == "null" {
		return q, ErrProductMissing
	}
	var p struct {
		ID     string         `json:"rest_id"`
		Prices []ProductPrice `json:"prices"`
	}
	if err := json.Unmarshal(r.Data.Product, &p); err != nil {
		return q, fmt.Errorf("unexpected product shape: %w", err)
	}
	q.RestID, q.Prices = p.ID, p.Prices
	var tree any
	if err := json.Unmarshal(r.Data.Product, &tree); err == nil {
		walkTextFields(tree, "", &q.TextFields)
	}
	sort.SliceStable(q.TextFields, func(i, j int) bool { return q.TextFields[i].Path < q.TextFields[j].Path })
	return q, nil
}

func isTextKey(key string) bool {
	k := strings.ToLower(key)
	for _, w := range []string{"name", "title", "description", "label"} {
		if strings.Contains(k, w) {
			return true
		}
	}
	return false
}

func walkTextFields(node any, path string, out *[]ProductTextField) {
	switch n := node.(type) {
	case map[string]any:
		for k, v := range n {
			p := k
			if path != "" {
				p = path + "." + k
			}
			if s, ok := v.(string); ok {
				if isTextKey(k) && strings.TrimSpace(s) != "" {
					*out = append(*out, ProductTextField{Path: p, Value: s})
				}
				continue
			}
			walkTextFields(v, p, out)
		}
	case []any:
		for i, v := range n {
			walkTextFields(v, fmt.Sprintf("%s[%d]", path, i), out)
		}
	}
}

// ProductName returns a product name only when the product object carries it
// directly as a top-level "name" or "product_name" string. Anything else is
// ambiguous and must be confirmed on the Stripe checkout page.
func (q ProductQuote) ProductName() (string, bool) {
	for _, key := range []string{"name", "product_name"} {
		for _, f := range q.TextFields {
			if f.Path == key && ValidPlanName(f.Value) {
				return f.Value, true
			}
		}
	}
	return "", false
}
