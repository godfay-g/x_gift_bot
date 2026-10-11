// Gift tiers. X decides the tier from the Stripe product; the site only labels
// codes and plans with it. A missing tier (older API responses) is Premium.
export type Tier = "premium" | "premium_plus";

export function normalizeTier(tier?: string | null): Tier {
  return tier === "premium_plus" ? "premium_plus" : "premium";
}

export function tierLabel(tier?: string | null): string {
  return normalizeTier(tier) === "premium_plus" ? "Premium+" : "Premium";
}

/** "6 个月 Premium+" */
export function giftLabel(tier: string | null | undefined, months: number): string {
  return `${months} 个月 ${tierLabel(tier)}`;
}

/** Stable select value for a (tier, months) plan. */
export function planKey(tier: string | null | undefined, months: number): string {
  return `${normalizeTier(tier)}:${months}`;
}

export function parsePlanKey(key: string): { tier: Tier; months: number } | null {
  const match = /^(premium|premium_plus):(\d{1,2})$/.exec(key);
  if (!match) return null;
  return { tier: match[1] as Tier, months: Number(match[2]) };
}

// Tier chips offered in the admin filter palette; values are filter tokens.
export const TIER_FILTER_OPTIONS: ReadonlyArray<{ value: Tier; label: string }> = [
  { value: "premium", label: "Premium" },
  { value: "premium_plus", label: "Premium+" },
];
