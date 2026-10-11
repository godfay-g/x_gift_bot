# Premium+ 赠送支持

本文说明 XGift 如何在原有 X Premium 赠送之外支持 **X Premium+** 赠送，以及运营者在上线前必须自行确认的事项。

> **重要：仓库没有内置任何 Premium+ 价格或商品名，默认目录也不含 Premium+。** 下文给出的 Premium+ 商品 ID 来自 X 网页公开前端脚本，价格需用 `xgift quote` 在自己的部署上只读查询，商品名只能在 Stripe 结账页确认。下文标注「需实测」的地方，代码目前只是按 Premium 的行为推断，尚未经过真实订单验证。

## 1. 设计概览

X 的赠送流程对 Premium 与 Premium+ 是同一套内部接口：

| 步骤 | 接口 | 作用 |
| --- | --- | --- |
| 资格 | `PremiumGiftingQuery` | 读取接收方 `rest_id` 与 `premium_gifting_eligible` |
| 报价 | `useSubscriptionProductDetailsByRestIdQuery`（变量 `stripeId` = 商品 ID） | 必须返回**唯一一个** `OneTime` 价格，且 `amount_local_micro == amount × 10000` |
| 建单 | `useOneTimePurchaseGiftMutation`（`external_product_id` = 商品 ID，`gift_recipient`） | 返回 Stripe Checkout 会话 |
| 付款 | Stripe Checkout | 付款前逐项核对商户、币种、金额、商品 ID、**商品名** |

**档位（tier）完全由 Stripe 商品 ID 决定**：同一个接口传入 Premium+ 的 `prod_...` 就是 Premium+ 赠送。因此本次改动的核心是让「商品目录 → 兑换码 → 订单」全链路都带上档位：

- **商品目录（catalog）**：每个套餐新增 `tier`（`premium` | `premium_plus`，缺省为 `premium`，兼容旧记录）与可选的 `name`（Stripe 结账页的**精确**商品名）。最多 4 个套餐；`(tier, months)` 组合唯一；同一商品 ID 不能出现在两个套餐中。
- **套餐解析**：所有代码路径按 `(tier, months)` 选择套餐（`Catalog.PlanFor(tier, months)`），不再只按月数。
- **订单记录（vault `checkout:*` / `public-checkout:*`）**：新增 `tier` 字段；旧记录没有该字段，一律视为 `premium`。所有「订单是否属于这个兑换码/套餐」的比对都包含档位。
- **兑换码（site.db `codes` 表）**：新增 `tier` 列（默认 `'premium'`），并放宽原来的 `months IN (3,6)` 约束为 `1–24`，以便存放 Premium+ 的时长（例如 12 个月）。实际能否生成某个档位/时长的兑换码由商品目录决定。
- **用户可见文案**：成功、资格不足等提示根据兑换码档位显示「Premium」或「Premium+」；兑换页在查询/兑换后显示「兑换码套餐：N 个月 Premium+」。
- **后台**：生成兑换码时从已配置套餐中选择「档位 + 时长」；列表、查询、客户详情、补单、统计（按档位+时长分组）都会显示档位；筛选栏提供 Premium / Premium+ 档位快捷按钮，表达式支持 `tier:premium_plus`。
- **Stripe 幂等键**：非 Premium 档位的订单会把档位加入幂等键推导；Premium 订单（含无 `tier` 的旧记录）的幂等键保持不变，已提交订单的对账校验不受影响。
- **CLI**：`xgift <用户名> --tier premium_plus --months 12 [--inspect|--pay]`；`--tier` 缺省为 `premium`。

### 为什么 Premium+ 必须配置 `name`

付款前的 Stripe 校验（`internal/checkout/stripe.go` 的 `guard`）要求结账页的 line item 名称与商品名称**完全等于** `Plan.Name()`，否则拒绝付款。Premium 沿用 X 现有命名 `Premium Gift - N months`；Premium+ 的命名无法预先得知（可能是 `Premium+ Gift - 12 months`，也可能是 `... - 1 year` 等），猜错会导致**每一单都在付款前被拒**。因此 `ParseCatalog` 要求 `premium_plus` 套餐必须提供 `name`：非空、≤120 字节、无控制字符、首尾无空格。Premium 套餐可以省略（使用默认名），也可以显式填写。

## 2. 如何获取 Premium+ 的商品 ID、价格与商品名

以下操作只读、不付款。请使用运营用的 X 账号，并通过与线上相同的出口（代理）访问，因为 X 按出口所在地区定价。

1. 在浏览器登录 x.com，打开 DevTools → Network，勾选 *Preserve log*，过滤 `graphql`。
2. 打开 `https://x.com/<任意未订阅用户>/gift-premium`（或在对方主页点礼物图标），在页面中选择 **Premium+**。
3. 在网络请求中找到：
   - `useSubscriptionProductDetailsByRestIdQuery`：请求变量里的 `stripeId` 就是**商品 ID**（`prod_...`）；响应 `web_subscription_product_details_by_rest_id.prices[]` 中 `price_type`、`currency_code`、`amount_local_micro` 给出价格（`amount_local_micro / 10000` 即目录中的 `amount`，单位为最小货币单位）。**必须恰好只有一个 `OneTime` 价格**，否则本程序会拒绝下单。
   - 若页面提供多个时长，每个时长各有一个 `stripeId`，分别记录。
4. 商品名（`name`）：
   - 方式 A：点「Gift & Pay」进入 Stripe 结账页**但不要付款**，在 Network 中查看 `payment_pages/<cs_live_...>/init` 的响应，`line_item_group.line_items[0].name` 与 `line_items[0].price.product.name` 即为所需名称（两者应一致）；或直接复制结账页上显示的商品名。随后关闭页面即可，未付款的会话会自然过期。
   - 方式 B：若 `useSubscriptionProductDetailsByRestIdQuery` 的响应中包含商品名称字段，可作为参考，但**以 Stripe 结账页的名称为准**。
   - 注意 `+` 号、空格、大小写、`month`/`months` 单复数都必须逐字一致。
5. 商户与币种：同一个 `init` 响应中的 `account_settings.account_id`（商户，默认目录为 `acct_1Ika5JA3KZ32dPo1`）和 `currency`（默认目录为 `bdt`）。**不要假设 Premium+ 与 Premium 相同**——目录中所有套餐共用一个商户和币种，如果 Premium+ 的商户或币种不同，本版本无法在同一目录中混用，请勿添加。

### 可用时长需在页面上确认

X 帮助中心与公开报道称官方赠送「目前仅提供年度（annual）Premium / Premium+」；而本仓库默认 Premium 目录使用的是 3/6 个月套餐（来自特定地区/币种的结账页）。两者并不一致，说明可选时长随地区、账号或时间而变。**请以你实际看到的赠送页为准**，只配置页面上真实存在且报价接口返回单一 `OneTime` 价格的时长。数据库允许 1–24 个月。

## 2.1 用 `xgift quote` 只读查价（推荐，无需浏览器）

网页上的赠送入口可能被 X 撤下或对某些账号不显示，但报价接口仍可读。`xgift quote` 使用部署机保管库里已有的 `cookies`、`api-auth` 和 `proxy`，启动与下单相同的内置 sing-box 地区出口，**只**发送一个 GET 查询 `useSubscriptionProductDetailsByRestIdQuery`（queryId `Se1Bp6zcNnuXYXRecV2qLA`，与下单前 `quote()` 完全相同的请求）。它不会调用资格查询、建单 mutation（`useOneTimePurchaseGiftMutation`）或任何 Stripe 接口；代码层有只读白名单守卫，误调用会在发出请求前被拒绝，并有单元测试覆盖。读失败沿用原有的重试/限流规则（429/5xx/403 最多 3 次，遵守 Retry-After），失败详情加密保存在 vault 的 `x-read-failure:*` 记录中。不占用 checkout 锁，可以在网站运行时执行。

Premium+ 三个商品 ID 来自 X 网页公开前端脚本中的正式商品表（键名分别为 `BlueVerifiedPlus3Months`、`BlueVerifiedPlus6Months`、`BlueVerifiedPlus`）。同一张表里的 Premium 3/6 个月 ID 与本仓库默认目录一致，但 Premium+ 这三个 ID 尚无第二个公开来源印证，以查价结果为准：

| 档位 | 时长 | 商品 ID |
| --- | --- | --- |
| Premium+ | 3 个月 | `prod_TJXL297k753Zvb` |
| Premium+ | 6 个月 | `prod_TJcPqt9ypBzEFQ` |
| Premium+ | 1 年（12 个月） | `prod_QH6h8qsTyMQrnI` |

```sh
./bin/xgift quote \
  --product prod_TJXL297k753Zvb \
  --product prod_TJcPqt9ypBzEFQ \
  --product prod_QH6h8qsTyMQrnI
# 需要看完整响应（只含响应体，不含任何请求头或 Cookie）时加 --raw
./bin/xgift quote --product prod_QH6h8qsTyMQrnI --raw
```

`--db`、`--password-file`（或 `XGIFT_PASSWORD_FILE`）、`--port` 与其他命令相同。也可写成 `--product id1,id2`。注意：`xgift quote` 现在是命令；如果真要给用户名为 `quote` 的账号下单，请写 `xgift @quote ...`。

每个商品会输出：`rest_id`；每个价格的 `amount_local_micro`、最小货币单位金额（`micro / 10000`，不能整除时会标出且不生成片段）、`currency_code`、`price_type`；以及响应中所有键名包含 name/title/description/label 的字符串字段。最后给出可直接粘贴进目录 `plans` 数组的片段（`tier`、`months`、`amount`、`product`），`name` 默认是 `"<fill from Stripe checkout>"`，只有响应顶层明确带有 `name` / `product_name` 时才会预填。**无论是否预填，`name` 都必须与 Stripe 结账页 line item 名称逐字核对后再写入**；`ParseCatalog` 会拒绝仍是 `<...>` 占位符的名称，避免误写入。

**价格取决于代理出口所在地区。** 默认配置从孟加拉出口，返回的是 BDT（`currency_code` 为 `BDT`，目录写 `bdt`）。换了出口国家，币种和金额都会变；目录里所有套餐共用一个 `currency`，必须与下单时的出口一致。

常见失败：HTTP 401/403 或 GraphQL `AuthorizationError` 表示未登录或 Cookie 过期（重新 `import-chrome` 或 `put --name cookies`）；HTTP 404 通常也是未登录 / Cookie 失效，或 X 更换了 queryId；响应里没有商品说明该 ID 在当前地区不可用或不存在。

## 3. 配置示例

同时包含 Premium 与 Premium+ 的目录（Premium+ 的 ID、金额、名称均为**占位示例**，必须替换为实测值）：

```json
{
  "merchant": "acct_1Ika5JA3KZ32dPo1",
  "currency": "bdt",
  "plans": [
    { "tier": "premium", "months": 3, "amount": 30000, "product": "prod_TJXJtpzqCpI36N" },
    { "tier": "premium", "months": 6, "amount": 60000, "product": "prod_TJXKKNJwZJIhCM" },
    { "tier": "premium_plus", "months": 12, "amount": 0, "product": "prod_替换为实测ID", "name": "替换为 Stripe 结账页的精确商品名" }
  ]
}
```

> 上例的 `amount: 0` 与中文占位会被 `ParseCatalog` 拒绝，这是故意的：请填入实测的正整数金额（最小货币单位）、真实 `prod_` ID 和商品名后再写入。

写入方式（二选一）：

```sh
# 1) 向导：选择默认目录或自定义目录后，回答「是否添加 Premium+ 赠送套餐？」为 y，
#    逐项粘贴时长、金额、prod_ ID 与 Stripe 商品名（没有任何默认值）。
./bin/xgift setup

# 2) 直接写入完整目录（会整体替换旧目录，并先校验）
./bin/xgift put --name catalog < catalog.json
./bin/xgift status   # 会列出每个套餐的 tier 与 months
```

只读核验（不建单、不付款）建议顺序：

```sh
./bin/xgift <测试用户名> --tier premium_plus --months 12           # 不带 --pay：只核对资格、报价并创建/核验结账会话，不提交付款
./bin/xgift <测试用户名> --tier premium_plus --months 12 --inspect # 读取该订单的 Stripe 状态
```

> 不带 `--pay` 仍会调用 X 的建单接口生成一个**未付款**的 Stripe 会话（不扣款），并占用该接收方的订单记录；请使用专门的测试账号。

## 4. 迁移说明

- **商品目录**：旧目录无需修改，缺少 `tier` 的套餐按 `premium` 处理。注意新版本新增两条校验：同一商品 ID 不能重复出现；`premium_plus` 必须有 `name`。
- **site.db**：首次启动新版本时自动执行一次性迁移 `code-tiers-v1`：在事务中重建 `codes` 表（新增 `tier TEXT NOT NULL DEFAULT 'premium'`，`months` 约束改为 `1–24`），逐行复制所有兑换码（ID、摘要、状态、绑定账号、批次、文件夹、进度、可复制标记全部保留），重建索引与批次触发器，校验行数和外键后提交。失败会整体回滚并拒绝启动。**升级前请照常备份数据目录**。迁移后旧版本程序仍可读取该表（多出的列会被忽略），但旧版本无法理解 Premium+ 兑换码，请勿回滚后继续兑换 Premium+ 码。
- **vault 订单记录**：不迁移。旧记录无 `tier` 即为 Premium；新记录写入 `tier`。
- **公开付款排队**：已保存的排队票据没有 `tier`，恢复时按 Premium 处理。
- **API**：`/api/admin/codes`（生成）、`/api/manual-link` 等请求新增可选 `tier` 字段，缺省为 `premium`，旧前端仍可工作；响应新增 `tier` / `tier_label`。

## 5. 资格检查（需实测）

`PremiumGiftingQuery` 只返回 `premium_gifting_eligible` 一个布尔值。代码目前对 Premium 和 Premium+ **都使用这个字段**（`internal/checkout/xapi.go` 中有 `TODO(premium-plus)`）。尚未验证：

- 该字段是否同样约束 Premium+ 赠送；
- 已订阅 Premium（或 Basic）的账号能否接收 Premium+ 赠送，反之亦然；
- X 公开说明「接收方须为未订阅用户」，且「即使接收方不符合条件，赠送也视为完成、不退款」——若资格判断不准确，Premium+ 的损失更大。

在完成实测前，建议只对确认未订阅的账号兑换 Premium+。

## 6. 风险

- **服务条款与封号**：本程序用运营者的 Cookie 调用 X 内部 GraphQL 接口并自动下单，违反或可能违反 X 服务条款，运营账号可能被限制或封禁；Premium+ 订单金额更高，更容易触发风控审查。
- **Stripe 风控**：「卡 × 代理节点」轮换、跨地区出口与频繁建单本身就是支付风控信号；Premium+ 单笔金额约为 Premium 年费的数倍，拒付、3DS 验证、发卡行冻结和 `do_not_try_again` 的概率与代价都更高。
- **客单价更高**：一次错误（商品名/价格配置错、资格判断错、重复付款）带来的损失更大；赠送不可退款。
- **价格变动**：X 会调整 Premium+ 价格（例如 2025-02-18 的调价）。报价与目录金额不一致时程序会拒绝下单，需及时更新目录。
- **接口变更**：GraphQL query ID、字段名或 Stripe 结账结构变化会导致失败（程序会在付款前拒绝，而不是盲目付款）。

## 7. 上线前检查清单（需实测）

1. 用只读报价确认 Premium+ 的商品 ID、单一 `OneTime` 价格、**商户与币种**与目录一致。
2. 从 Stripe 结账页确认精确商品名并写入 `name`。
3. 确认页面实际提供的时长，只配置这些时长。
4. **第一笔真实订单使用小号/测试接收账号**，并由运营者在场：先不带 `--pay` 生成并 `--inspect` 核验，再决定是否付款；付款后在 X 上确认接收方获得的是 Premium+ 而非 Premium、时长正确。
5. 观察资格字段对已订阅 Premium 的账号的表现，必要时更新本文与代码。
