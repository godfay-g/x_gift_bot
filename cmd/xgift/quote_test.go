package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"xgift/internal/checkout"
)

func parsed(t *testing.T, body string) checkout.ProductQuote {
	t.Helper()
	q, err := checkout.ParseProductDetails([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestRunQuotePrintsPricesFieldsAndPlanSnippet(t *testing.T) {
	bodies := map[string]string{
		"prod_TJXL297k753Zvb": `{"data":{"web_subscription_product_details_by_rest_id":{"rest_id":"prod_TJXL297k753Zvb","title":"Premium+","prices":[{"amount_local_micro":15000000000,"currency_code":"BDT","price_type":"OneTime"}]}}}`,
		"prod_QH6h8qsTyMQrnI": `{"data":{"web_subscription_product_details_by_rest_id":{"rest_id":"prod_QH6h8qsTyMQrnI","prices":[{"amount_local_micro":12345,"currency_code":"BDT","price_type":"OneTime"}]}}}`,
	}
	var buf bytes.Buffer
	err := runQuote(context.Background(), &buf, []string{"prod_TJXL297k753Zvb", "prod_QH6h8qsTyMQrnI"}, false, func(_ context.Context, ids []string) ([]checkout.ProductQuote, error) {
		var out []checkout.ProductQuote
		for _, id := range ids {
			out = append(out, parsed(t, bodies[id]))
		}
		return out, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s := buf.String()
	for _, want := range []string{
		"== prod_TJXL297k753Zvb ==",
		"amount_local_micro=15000000000 amount(最小单位)=1500000 currency_code=BDT price_type=OneTime",
		`title = "Premium+"`,
		"amount_local_micro=12345 amount(最小单位)=不可整除",
		"Stripe 结账页",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("output missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "原始响应") {
		t.Fatal("raw printed without --raw")
	}
	start := strings.Index(s, "[\n")
	end := strings.Index(s, "\n]")
	var plans []catalogPlan
	if start < 0 || end < 0 || json.Unmarshal([]byte(s[start:end+2]), &plans) != nil {
		t.Fatalf("no plan JSON:\n%s", s)
	}
	// Only the divisible one-time price becomes a plan; title is not a name.
	if len(plans) != 1 || plans[0] != (catalogPlan{Tier: "premium_plus", Months: 3, Amount: 1500000, Product: "prod_TJXL297k753Zvb", Name: nameToFill}) {
		t.Fatalf("plans = %+v", plans)
	}
}

func TestRunQuoteRawAndExplicitName(t *testing.T) {
	body := `{"data":{"web_subscription_product_details_by_rest_id":{"rest_id":"prod_TJcPqt9ypBzEFQ","name":"Premium+ Gift - 6 months","prices":[{"amount_local_micro":1000000,"currency_code":"BDT","price_type":"OneTime"}]}}}`
	var buf bytes.Buffer
	err := runQuote(context.Background(), &buf, []string{"prod_TJcPqt9ypBzEFQ"}, true, func(context.Context, []string) ([]checkout.ProductQuote, error) {
		return []checkout.ProductQuote{parsed(t, body)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s := buf.String()
	if !strings.Contains(s, "原始响应") || !strings.Contains(s, `"web_subscription_product_details_by_rest_id"`) {
		t.Fatalf("raw missing:\n%s", s)
	}
	if !strings.Contains(s, `"name": "Premium+ Gift - 6 months"`) || !strings.Contains(s, `"months": 6`) {
		t.Fatalf("plan name/months missing:\n%s", s)
	}
}

func TestRunQuoteRejectsInvalidIDsBeforeFetching(t *testing.T) {
	called := false
	fetch := func(context.Context, []string) ([]checkout.ProductQuote, error) { called = true; return nil, nil }
	for _, ids := range [][]string{nil, {"price_1"}, {"prod_"}, {"prod_a b"}, {"prod_A1", "prod_A1"}} {
		if err := runQuote(context.Background(), &bytes.Buffer{}, ids, false, fetch); err == nil {
			t.Fatalf("accepted %v", ids)
		}
	}
	if called {
		t.Fatal("fetch called for invalid input")
	}
	var p productList
	if p.Set("prod_ok1,prod_bad!") == nil {
		t.Fatal("flag accepted invalid ID")
	}
	if err := p.Set("prod_A1, prod_B2"); err != nil || len(p) != 3 {
		t.Fatalf("flag parse: %v %v", err, p)
	}
}

func TestQuoteErrorMessagesAreChinese(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("prod_A1: %w", &checkout.XHTTPStatusError{Status: 404}), "404"},
		{fmt.Errorf("prod_A1: %w", &checkout.XHTTPStatusError{Status: 401}), "Cookie 可能已过期"},
		{&checkout.XGraphQLError{Messages: []string{"AuthorizationError"}}, "未登录或 Cookie 失效"},
		{checkout.ErrProductMissing, "商品 ID 不存在"},
		{checkout.ErrReadOnlyViolation, "未发送任何请求"},
		{errors.New("required X cookies missing"), "没有可用的 X 登录信息"},
		{errors.New("unexpected product shape"), "查价失败"},
	} {
		if got := quoteError(tc.err).Error(); !strings.Contains(got, tc.want) {
			t.Fatalf("quoteError(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}
