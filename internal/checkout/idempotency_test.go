package checkout

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// legacyIdempotency is the derivation used before tiers existed.
func legacyIdempotency(operation, session, recipient, months string) string {
	sum := sha256.Sum256([]byte("xgift-v1:" + operation + ":" + session + ":" + recipient + ":" + months))
	return "xgift-" + hex.EncodeToString(sum[:])
}

func TestIdempotencyKeepsPremiumKeysAndBindsOtherTiers(t *testing.T) {
	legacy := Record{SessionID: "cs_live_TEST", RecipientID: "42", Months: 6}
	premium := legacy
	premium.Tier = TierPremium
	plus := legacy
	plus.Tier = TierPremiumPlus

	for _, op := range []string{"confirm", "method"} {
		want := legacyIdempotency(op, "cs_live_TEST", "42", "6")
		if got := idempotency(&legacy, op); got != want {
			t.Fatalf("legacy %s key changed: %s != %s", op, got, want)
		}
		if got := idempotency(&premium, op); got != want {
			t.Fatalf("explicit premium %s key differs from legacy: %s != %s", op, got, want)
		}
		if got := idempotency(&plus, op); got == want || got == idempotency(&premium, op) {
			t.Fatalf("premium_plus %s key must differ from premium: %s", op, got)
		}
		if got, again := idempotency(&plus, op), idempotency(&plus, op); got != again {
			t.Fatalf("premium_plus %s key is not deterministic", op)
		}
	}

	// Manual recovery attempts keep their legacy key for Premium, and stay
	// distinct per tier and per attempt.
	legacy.ManualRecovery, legacy.RecoveryAttempts = true, 2
	plus.ManualRecovery, plus.RecoveryAttempts = true, 2
	if got, want := idempotency(&legacy, "confirm"), legacyIdempotency("confirm:manual:2", "cs_live_TEST", "42", "6"); got != want {
		t.Fatalf("legacy manual recovery key changed: %s != %s", got, want)
	}
	if idempotency(&plus, "confirm") == idempotency(&legacy, "confirm") {
		t.Fatal("premium_plus manual recovery key must differ from premium")
	}
	plus.RecoveryAttempts = 3
	if idempotency(&plus, "confirm") == idempotency(&legacy, "confirm") {
		t.Fatal("recovery attempts must change the key")
	}
}
