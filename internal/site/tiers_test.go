package site

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"xgift/internal/checkout"
)

const tierTestCatalog = `{"merchant":"acct_TESTMERCHANT","currency":"usd","plans":[
 {"months":3,"amount":30000,"product":"prod_TEST3MO"},
 {"tier":"premium","months":6,"amount":60000,"product":"prod_TEST6MO"},
 {"tier":"premium_plus","months":12,"amount":240000,"product":"prod_PLUS12MO","name":"Premium+ Gift - 12 months"}]}`

// legacySiteDB builds a site.db exactly as the pre-Premium+ release left it:
// months CHECK(3,6), progress/folder/copyable columns, batch triggers.
func legacySiteDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE codes (
 id TEXT PRIMARY KEY, hash TEXT NOT NULL UNIQUE, hint TEXT NOT NULL, batch TEXT NOT NULL,
 months INTEGER NOT NULL CHECK(months IN (3,6)),
 status TEXT NOT NULL CHECK(status IN ('active','processing','succeeded','review','revoked')),
 username TEXT NOT NULL DEFAULT '', recipient_id TEXT UNIQUE,
 message TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL, updated INTEGER NOT NULL
 ); CREATE INDEX codes_created ON codes(created);`,
		`ALTER TABLE codes ADD COLUMN progress INTEGER NOT NULL DEFAULT 0`,
		`CREATE TABLE folders (id TEXT PRIMARY KEY, name TEXT NOT NULL COLLATE NOCASE UNIQUE, created INTEGER NOT NULL, updated INTEGER NOT NULL)`,
		`ALTER TABLE codes ADD COLUMN folder_id TEXT REFERENCES folders(id) ON DELETE SET NULL`,
		`CREATE INDEX codes_folder_created ON codes(folder_id,created)`,
		`CREATE TABLE site_migrations(name TEXT PRIMARY KEY)`,
		`ALTER TABLE codes ADD COLUMN copyable INTEGER NOT NULL DEFAULT 0`,
		`INSERT INTO site_migrations(name) VALUES('batch-folders-v2')`,
		batchTriggers,
		`INSERT INTO folders VALUES('` + strings.Repeat("f", 32) + `','batch-a',1,1)`,
		`INSERT INTO codes(id,hash,hint,batch,months,status,username,recipient_id,message,created,updated,progress,folder_id,copyable) VALUES
 ('` + strings.Repeat("1", 32) + `','h1','AAAA','batch-a',3,'active','','','',10,10,0,'` + strings.Repeat("f", 32) + `',1),
 ('` + strings.Repeat("2", 32) + `','h2','BBBB','',6,'review','alice','42','kept',11,12,80,NULL,0)`,
	} {
		if _, err = db.Exec(statement); err != nil {
			t.Fatalf("%v: %s", err, statement)
		}
	}
}

func TestMigrationKeepsOldCodesAsPremium(t *testing.T) {
	path := filepath.Join(t.TempDir(), "site.db")
	legacySiteDB(t, path)
	db, err := openSiteDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	type row struct {
		months                                 int
		tier, status, username, message, batch string
		folder                                 sql.NullString
		progress, copyable                     int
	}
	read := func(id string) row {
		var r row
		if err := db.QueryRow(`SELECT months,tier,status,username,message,batch,folder_id,progress,copyable FROM codes WHERE id=?`, id).Scan(&r.months, &r.tier, &r.status, &r.username, &r.message, &r.batch, &r.folder, &r.progress, &r.copyable); err != nil {
			t.Fatal(err)
		}
		return r
	}
	a, b := read(strings.Repeat("1", 32)), read(strings.Repeat("2", 32))
	if a.tier != "premium" || a.months != 3 || a.status != "active" || a.batch != "batch-a" || a.folder.String != strings.Repeat("f", 32) || a.copyable != 1 {
		t.Fatalf("active code changed: %+v", a)
	}
	if b.tier != "premium" || b.months != 6 || b.status != "review" || b.username != "alice" || b.message != "kept" || b.progress != 80 || b.folder.Valid {
		t.Fatalf("review code changed: %+v", b)
	}
	// Batch triggers survive the rebuild.
	if _, err = db.Exec(`UPDATE folders SET name='renamed' WHERE id=?`, strings.Repeat("f", 32)); err != nil {
		t.Fatal(err)
	}
	if read(strings.Repeat("1", 32)).batch != "renamed" {
		t.Fatal("batch_rename trigger lost")
	}
	// Premium+ durations outside the old (3,6) check can now be stored...
	if _, err = db.Exec(`INSERT INTO codes(id,hash,hint,batch,months,tier,status,created,updated) VALUES('x','hx','XXXX','',12,'premium_plus','active',1,1)`); err != nil {
		t.Fatal(err)
	}
	// ...but unknown tiers, missing folders and absurd durations are still rejected.
	for _, bad := range []string{
		`INSERT INTO codes(id,hash,hint,batch,months,tier,status,created,updated) VALUES('y','hy','YYYY','',6,'gold','active',1,1)`,
		`INSERT INTO codes(id,hash,hint,batch,months,tier,status,created,updated) VALUES('z','hz','ZZZZ','',36,'premium','active',1,1)`,
		`INSERT INTO codes(id,hash,hint,batch,months,tier,status,created,updated,folder_id) VALUES('w','hw','WWWW','',6,'premium','active',1,1,'missing')`,
	} {
		if _, err = db.Exec(bad); err == nil {
			t.Fatalf("accepted invalid row: %s", bad)
		}
	}
	db.Close()
	// Reopening is idempotent and keeps the data.
	db, err = openSiteDB(path)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err = db.QueryRow(`SELECT COUNT(*) FROM codes`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("rows after reopen: %d %v", n, err)
	}
}

func TestFreshDatabaseHasTierColumn(t *testing.T) {
	db, err := openSiteDB(filepath.Join(t.TempDir(), "site.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`INSERT INTO codes(id,hash,hint,batch,months,status,created,updated) VALUES('a','ha','AAAA','',6,'active',1,1)`); err != nil {
		t.Fatal(err)
	}
	var tier string
	if err = db.QueryRow(`SELECT tier FROM codes WHERE id='a'`).Scan(&tier); err != nil || tier != "premium" {
		t.Fatalf("default tier %q %v", tier, err)
	}
}

func tierServer(t *testing.T) *server {
	t.Helper()
	s := checkFixture(t)
	db, err := openSiteDB(filepath.Join(t.TempDir(), "site.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s.db = db
	if err = s.vault.Put("catalog", []byte(tierTestCatalog)); err != nil {
		t.Fatal(err)
	}
	return s
}

func generateCodes(s *server, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/admin/codes", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.generate(w, req)
	return w
}

func TestGenerateCodesForConfiguredTierAndMonths(t *testing.T) {
	s := tierServer(t)
	for _, tc := range []struct {
		body string
		code int
		tier string
	}{
		{`{"months":6,"count":1}`, 201, "premium"},
		{`{"tier":"premium","months":3,"count":1}`, 201, "premium"},
		{`{"tier":"premium_plus","months":12,"count":2}`, 201, "premium_plus"},
		{`{"tier":"premium_plus","months":6,"count":1}`, 400, ""},
		{`{"tier":"premium","months":12,"count":1}`, 400, ""},
		{`{"tier":"gold","months":6,"count":1}`, 400, ""},
	} {
		w := generateCodes(s, tc.body)
		if w.Code != tc.code {
			t.Fatalf("%s: status=%d body=%s", tc.body, w.Code, w.Body.String())
		}
		if tc.code == 201 {
			var out struct {
				Codes []string `json:"codes"`
				Tier  string   `json:"tier"`
			}
			json.Unmarshal(w.Body.Bytes(), &out)
			if out.Tier != tc.tier {
				t.Fatalf("%s: tier=%q", tc.body, out.Tier)
			}
			c, err := s.find(out.Codes[0])
			if err != nil || c.Tier != tc.tier {
				t.Fatalf("%s: stored %+v %v", tc.body, c, err)
			}
		}
	}
	var plus int
	s.db.QueryRow(`SELECT COUNT(*) FROM codes WHERE tier='premium_plus' AND months=12`).Scan(&plus)
	if plus != 2 {
		t.Fatalf("premium_plus codes = %d", plus)
	}
	// Stats group by tier and months.
	w := httptest.NewRecorder()
	s.stats(w, httptest.NewRequest("GET", "/api/admin/stats", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"tier":"premium_plus","tier_label":"Premium+","months":12,"total":2`) {
		t.Fatalf("stats: %s", w.Body.String())
	}
}

func TestGenerateRequiresCatalog(t *testing.T) {
	s := tierServer(t)
	s.vault.Put("catalog", []byte(`not json`))
	if w := generateCodes(s, `{"months":6,"count":1}`); w.Code != 503 {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestStatusAndSuccessMessageUseCodeTier(t *testing.T) {
	s := tierServer(t)
	s.payments = true
	s.lockPath = filepath.Join(t.TempDir(), "checkout.lock")
	code := "XG-" + strings.Repeat("B", 48)
	if _, err := s.db.Exec(`INSERT INTO codes(id,hash,hint,batch,months,tier,status,username,recipient_id,message,created,updated,progress) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, strings.Repeat("c", 32), hash(code), "BBBB", "", 12, "premium_plus", "review", "recipient", "1234", "original", 1, 2, 90); err != nil {
		t.Fatal(err)
	}
	put := func(r checkout.Record) {
		raw, _ := json.Marshal(r)
		if err := s.vault.Put("checkout:1234", raw); err != nil {
			t.Fatal(err)
		}
	}
	// A succeeded order of the wrong tier (even with the same months) never
	// completes a Premium+ code.
	put(checkout.Record{Username: "recipient", RecipientID: "1234", Months: 12, Amount: 240000, Currency: "USD", ProductID: "prod_PLUS12MO", SessionID: "cs_live_A1", URL: "https://checkout.stripe.com/c/pay/cs_live_A1", Status: "succeeded"})
	c, _ := s.find(code)
	s.reconcileStatus(t.Context(), &c)
	if c.Status != "review" {
		t.Fatalf("legacy premium order completed a premium_plus code: %+v", c)
	}
	put(checkout.Record{Username: "recipient", RecipientID: "1234", Tier: checkout.TierPremiumPlus, Months: 12, Amount: 240000, Currency: "USD", ProductID: "prod_PLUS12MO", SessionID: "cs_live_A1", URL: "https://checkout.stripe.com/c/pay/cs_live_A1", Status: "succeeded"})
	c, _ = s.find(code)
	s.reconcileStatus(t.Context(), &c)
	if c.Status != "succeeded" || !strings.Contains(c.Message, "12 个月 Premium+") {
		t.Fatalf("premium_plus success message: %+v", c)
	}
	body, _ := json.Marshal(map[string]string{"code": code, "username": "recipient"})
	req := httptest.NewRequest("POST", "/api/status", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.status(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"tier":"premium_plus"`) || !strings.Contains(w.Body.String(), `"tier_label":"Premium+"`) {
		t.Fatalf("status response: %s", w.Body.String())
	}
}

func TestRedeemMessageNamesTier(t *testing.T) {
	if msg := redeemMessage(nil, checkout.ErrNotEligible, checkout.TierPremiumPlus); !strings.Contains(msg, "Premium+") {
		t.Fatal(msg)
	}
	if msg := redeemMessage(nil, checkout.ErrNotEligible, checkout.TierPremium); !strings.Contains(msg, "Premium 赠送") || strings.Contains(msg, "Premium+") {
		t.Fatal(msg)
	}
}

func TestQueueTicketsWithoutTierRestoreAsPremium(t *testing.T) {
	s := checkFixture(t)
	s.vault.Put("catalog", []byte(tierTestCatalog))
	q := manualLinkRequest{Username: "alice", Months: 6}
	if q.tier() != checkout.TierPremium {
		t.Fatal("empty request tier is not premium")
	}
	plans := httptest.NewRecorder()
	s.manualLinkPlans(plans, httptest.NewRequest("GET", "/api/admin/manual-link/plans", nil))
	if !strings.Contains(plans.Body.String(), `"tier":"premium_plus","tier_label":"Premium+","months":12`) || !strings.Contains(plans.Body.String(), `"tier":"premium","tier_label":"Premium","months":3`) {
		t.Fatalf("plans: %s", plans.Body.String())
	}
}
