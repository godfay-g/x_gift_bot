package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"xgift/internal/checkout"
)

// productList collects repeated --product flags.
type productList []string

func (p *productList) String() string { return strings.Join(*p, ",") }
func (p *productList) Set(s string) error {
	for _, id := range strings.Split(s, ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if !checkout.ValidProductID(id) {
			return fmt.Errorf("商品 ID 无效：%q（必须形如 prod_ 加字母数字）", id)
		}
		*p = append(*p, id)
	}
	return nil
}

// knownProduct maps product IDs taken from X's public client JS product map
// to the tier and duration they represent there. It only labels output; the
// price itself always comes from the live read-only query.
type knownProduct struct {
	Tier   string
	Months int
	Source string
}

var knownProducts = map[string]knownProduct{
	"prod_TJXJtpzqCpI36N": {"premium", 3, "仓库默认 Premium 目录"},
	"prod_TJXKKNJwZJIhCM": {"premium", 6, "仓库默认 Premium 目录"},
	"prod_TJXL297k753Zvb": {"premium_plus", 3, "X 网页公开前端脚本商品表 BlueVerifiedPlus3Months"},
	"prod_TJcPqt9ypBzEFQ": {"premium_plus", 6, "X 网页公开前端脚本商品表 BlueVerifiedPlus6Months"},
	"prod_QH6h8qsTyMQrnI": {"premium_plus", 12, "X 网页公开前端脚本商品表 BlueVerifiedPlus"},
}

const nameToFill = "<fill from Stripe checkout>"

// catalogPlan mirrors one catalog plan entry for copy/paste.
type catalogPlan struct {
	Tier    string `json:"tier"`
	Months  int    `json:"months"`
	Amount  int64  `json:"amount"`
	Product string `json:"product"`
	Name    string `json:"name,omitempty"`
}

// quoteFunc is replaced in tests; production uses checkout.QuoteProducts.
type quoteFunc func(ctx context.Context, products []string) ([]checkout.ProductQuote, error)

func runQuote(ctx context.Context, w io.Writer, products []string, raw bool, fetch quoteFunc) error {
	if len(products) == 0 {
		return errors.New("quote 需要至少一个 --product prod_XXXX")
	}
	seen := map[string]bool{}
	for _, id := range products {
		if !checkout.ValidProductID(id) {
			return fmt.Errorf("商品 ID 无效：%q（必须形如 prod_ 加字母数字）", id)
		}
		if seen[id] {
			return fmt.Errorf("商品 ID 重复：%s", id)
		}
		seen[id] = true
	}
	quotes, err := fetch(ctx, products)
	var plans []catalogPlan
	currencies := map[string]bool{}
	for _, q := range quotes {
		printQuote(w, q, raw)
		if p, cur, ok := planFor(q); ok {
			plans = append(plans, p)
			currencies[cur] = true
		}
	}
	if len(plans) > 0 {
		fmt.Fprintln(w, "== 可粘贴的目录套餐片段（plans 数组元素）==")
		b, _ := json.MarshalIndent(plans, "", "  ")
		fmt.Fprintln(w, string(b))
		if len(currencies) > 1 {
			fmt.Fprintln(w, "警告：这些商品的币种不一致，同一个目录只能使用一种 currency。")
		}
		fmt.Fprintln(w, "警告：name 必须与 Stripe 结账页的 line item 名称逐字一致；即使上面已填，也要在结账页确认后再写入目录。")
		fmt.Fprintln(w, "警告：价格取决于本次代理出口所在地区（默认配置从孟加拉出口时为 BDT）；目录的 currency 必须与此一致。")
	}
	if err != nil {
		return quoteError(err)
	}
	return nil
}

func planFor(q checkout.ProductQuote) (catalogPlan, string, bool) {
	var oneTime []checkout.ProductPrice
	for _, p := range q.Prices {
		if p.PriceType == "OneTime" {
			oneTime = append(oneTime, p)
		}
	}
	if len(oneTime) != 1 || len(q.Prices) != 1 {
		return catalogPlan{}, "", false
	}
	minor, ok := oneTime[0].Minor()
	if !ok {
		return catalogPlan{}, "", false
	}
	known, isKnown := knownProducts[q.RestID]
	p := catalogPlan{Tier: known.Tier, Months: known.Months, Amount: minor, Product: q.RestID, Name: nameToFill}
	if !isKnown {
		p.Tier, p.Months = "<premium|premium_plus>", 0
	}
	if name, ok := q.ProductName(); ok {
		p.Name = name
	}
	return p, strings.ToLower(oneTime[0].CurrencyCode), true
}

func printQuote(w io.Writer, q checkout.ProductQuote, raw bool) {
	fmt.Fprintf(w, "== %s ==\n", q.RestID)
	if k, ok := knownProducts[q.RestID]; ok {
		fmt.Fprintf(w, "已知映射：tier=%s，%d 个月（来源：%s）\n", k.Tier, k.Months, k.Source)
	}
	if len(q.Prices) == 0 {
		fmt.Fprintln(w, "价格：响应中没有 prices")
	}
	for i, p := range q.Prices {
		minor := "不可整除，不能直接写入目录"
		if m, ok := p.Minor(); ok {
			minor = fmt.Sprint(m)
		}
		fmt.Fprintf(w, "价格 %d：amount_local_micro=%d amount(最小单位)=%s currency_code=%s price_type=%s\n", i+1, p.AmountLocalMicro, minor, p.CurrencyCode, p.PriceType)
	}
	if len(q.Prices) != 1 || (len(q.Prices) == 1 && q.Prices[0].PriceType != "OneTime") {
		fmt.Fprintln(w, "注意：下单流程要求恰好一个 OneTime 价格，这个商品当前不满足，不会生成目录片段。")
	}
	if len(q.TextFields) == 0 {
		fmt.Fprintln(w, "名称类字段：响应中没有 name/title/description/label 字段")
	} else {
		fmt.Fprintln(w, "名称类字段：")
		for _, f := range q.TextFields {
			fmt.Fprintf(w, "  %s = %q\n", f.Path, f.Value)
		}
	}
	if raw {
		fmt.Fprintln(w, "原始响应：")
		var buf bytes.Buffer
		if json.Indent(&buf, q.Raw, "", "  ") == nil {
			fmt.Fprintln(w, buf.String())
		} else {
			fmt.Fprintln(w, string(q.Raw))
		}
	}
}

// quoteError turns read failures into operator-facing Chinese messages.
func quoteError(err error) error {
	var status *checkout.XHTTPStatusError
	var gql *checkout.XGraphQLError
	switch {
	case errors.Is(err, checkout.ErrReadOnlyViolation):
		return fmt.Errorf("内部保护拦截了非只读请求，未发送任何请求：%w", err)
	case errors.As(err, &status) && (status.Status == 401 || status.Status == 403):
		return fmt.Errorf("X 拒绝访问（HTTP %d）：Cookie 可能已过期或未登录，请重新导入 cookies（import-chrome 或 put --name cookies）后再试：%w", status.Status, err)
	case errors.As(err, &status) && status.Status == 404:
		return fmt.Errorf("X 返回 404：通常是未登录 / Cookie 失效，或 queryId 已被 X 更换，也可能商品 ID 不存在：%w", err)
	case errors.As(err, &status) && status.Status == 429:
		return fmt.Errorf("X 限流（HTTP 429），已按重试规则重试仍失败，请稍后再试：%w", err)
	case errors.As(err, &status):
		return fmt.Errorf("X 返回 HTTP %d：%w", status.Status, err)
	case errors.As(err, &gql):
		msg := strings.Join(gql.Messages, "；")
		if strings.Contains(strings.ToLower(msg), "authoriz") || strings.Contains(strings.ToLower(msg), "auth") {
			return fmt.Errorf("X 拒绝查询（未登录或 Cookie 失效）：%s：%w", msg, err)
		}
		return fmt.Errorf("X 拒绝查询：%s：%w", msg, err)
	case errors.Is(err, checkout.ErrProductMissing):
		return fmt.Errorf("X 没有返回这个商品，可能商品 ID 不存在或当前地区不提供：%w", err)
	case strings.Contains(err.Error(), "cookie") || strings.Contains(err.Error(), "authentication metadata"):
		return fmt.Errorf("保管库里没有可用的 X 登录信息（cookies / api-auth），请先 import-chrome 或 put --name cookies / api-auth：%w", err)
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("已取消：%w", err)
	}
	return fmt.Errorf("查价失败（详细响应已加密保存在 vault 的 x-read-failure 记录中）：%w", err)
}
