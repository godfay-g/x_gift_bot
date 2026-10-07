package site

import (
	"context"
	"database/sql"
	"errors"
	"xgift/internal/checkout"
)

// batchTriggers keep codes.batch in sync with folder names. They are dropped
// and recreated around the codes table rebuild below.
const batchTriggers = `CREATE TRIGGER IF NOT EXISTS batch_rename AFTER UPDATE OF name ON folders BEGIN UPDATE codes SET batch=NEW.name WHERE folder_id=NEW.id; END;
 CREATE TRIGGER IF NOT EXISTS batch_move AFTER UPDATE OF folder_id ON codes BEGIN UPDATE codes SET batch=COALESCE((SELECT name FROM folders WHERE id=NEW.folder_id),'') WHERE id=NEW.id; END;`

// codesV3 adds the gift tier and relaxes the original months IN (3,6) check
// so Premium+ plans (whose durations may differ, e.g. 12 months) can be
// stored. Which tier/months are actually redeemable is decided by the catalog.
const codesV3 = `CREATE TABLE codes_v3 (
 id TEXT PRIMARY KEY, hash TEXT NOT NULL UNIQUE, hint TEXT NOT NULL, batch TEXT NOT NULL,
 months INTEGER NOT NULL CHECK(months BETWEEN 1 AND 24),
 status TEXT NOT NULL CHECK(status IN ('active','processing','succeeded','review','revoked')),
 username TEXT NOT NULL DEFAULT '', recipient_id TEXT UNIQUE,
 message TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL, updated INTEGER NOT NULL,
 progress INTEGER NOT NULL DEFAULT 0,
 folder_id TEXT REFERENCES folders(id) ON DELETE SET NULL,
 copyable INTEGER NOT NULL DEFAULT 0,
 tier TEXT NOT NULL DEFAULT 'premium' CHECK(tier IN ('premium','premium_plus'))
 )`

const codesV3Columns = `id,hash,hint,batch,months,status,username,recipient_id,message,created,updated,progress,folder_id,copyable`

// migrateCodeTiers runs once after the folder/batch migrations. Every
// existing code keeps its id, hash, status, binding and folder, and becomes
// tier 'premium' — the only tier that existed before this migration.
func migrateCodeTiers(db *sql.DB) (err error) {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var done int
	if err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_migrations WHERE name='code-tiers-v1'`).Scan(&done); err != nil || done > 0 {
		return err
	}
	// SQLite requires foreign keys off (outside a transaction) to rebuild a
	// table; the rebuilt rows are checked explicitly before committing.
	if _, err = conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer func() {
		if _, e := conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`); e != nil && err == nil {
			err = e
		}
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	hasTier := false
	rows, err := tx.Query("PRAGMA table_info(codes)")
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid, required, primary int
		var name, typ string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &typ, &required, &defaultValue, &primary); err != nil {
			rows.Close()
			return err
		}
		if name == "tier" {
			hasTier = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var before int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM codes`).Scan(&before); err != nil {
		return err
	}
	tierSource := "'premium'"
	if hasTier {
		tierSource = "CASE WHEN tier='premium_plus' THEN 'premium_plus' ELSE 'premium' END"
	}
	for _, statement := range []string{
		`DROP TABLE IF EXISTS codes_v3`,
		codesV3,
		`INSERT INTO codes_v3(` + codesV3Columns + `,tier) SELECT ` + codesV3Columns + `,` + tierSource + ` FROM codes`,
		`DROP TRIGGER IF EXISTS batch_rename`,
		`DROP TRIGGER IF EXISTS batch_move`,
		`DROP TABLE codes`,
		`ALTER TABLE codes_v3 RENAME TO codes`,
		`CREATE INDEX IF NOT EXISTS codes_created ON codes(created)`,
		`CREATE INDEX IF NOT EXISTS codes_folder_created ON codes(folder_id,created)`,
		batchTriggers,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	var after int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM codes`).Scan(&after); err != nil {
		return err
	}
	if after != before {
		return errors.New("code tier migration lost rows; database left unchanged")
	}
	violations, err := tx.Query(`PRAGMA foreign_key_check(codes)`)
	if err != nil {
		return err
	}
	bad := violations.Next()
	violations.Close()
	if bad {
		return errors.New("code tier migration found invalid folder references; database left unchanged")
	}
	if _, err = tx.Exec(`INSERT INTO site_migrations(name) VALUES('code-tiers-v1')`); err != nil {
		return err
	}
	return tx.Commit()
}

// codeTier reads a stored tier; unknown values fall back to Premium only for
// display, never for plan selection (PlanFor rejects them).
func codeTier(s string) checkout.Tier { return checkout.Tier(s).Normalize() }

// sameGift reports whether a checkout record is the order bound to this code:
// same recipient, username, tier and duration.
func sameGift(r *checkout.Record, c *codeRow) bool {
	return r != nil && r.RecipientID == c.RecipientID && r.Username == c.Username && r.Months == c.Months && r.PlanTier() == codeTier(c.Tier)
}

// giftDescription is the Chinese "N 个月 Premium/Premium+" text for a code.
func (c *codeRow) giftDescription() string {
	return checkout.GiftDescription(codeTier(c.Tier), c.Months)
}

// parseRequestTier normalises a tier from an API request; empty means Premium
// so older admin pages and saved queue tickets keep working.
func parseRequestTier(s string) (checkout.Tier, bool) { return checkout.ParseTier(s) }
