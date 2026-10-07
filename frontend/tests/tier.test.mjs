import test from 'node:test';
import assert from 'node:assert/strict';
import { giftLabel, normalizeTier, parsePlanKey, planKey, tierLabel } from '../src/tier.ts';
import { parseFilter } from '../src/filter.ts';

test('missing or unknown tier is shown as Premium', () => {
  assert.equal(tierLabel(undefined), 'Premium');
  assert.equal(tierLabel('premium'), 'Premium');
  assert.equal(tierLabel('premium_plus'), 'Premium+');
  assert.equal(normalizeTier('gold'), 'premium');
  assert.equal(giftLabel('premium_plus', 12), '12 个月 Premium+');
  assert.equal(giftLabel(undefined, 6), '6 个月 Premium');
});

test('plan keys round-trip tier and months', () => {
  assert.equal(planKey(undefined, 6), 'premium:6');
  assert.deepEqual(parsePlanKey(planKey('premium_plus', 12)), { tier: 'premium_plus', months: 12 });
  assert.equal(parsePlanKey('gold:6'), null);
  assert.equal(parsePlanKey('premium:'), null);
});

test('filter supports tier conditions', () => {
  const plus = { batch: '', status: 'active', months: 12, tier: 'premium_plus' };
  const legacy = { batch: '', status: 'active', months: 6 };
  assert.equal(parseFilter('tier:premium_plus')(plus), true);
  assert.equal(parseFilter('tier:plus')(legacy), false);
  assert.equal(parseFilter('tier:premium')(legacy), true);
  assert.equal(parseFilter('tier!=premium and months:12')(plus), true);
  assert.throws(() => parseFilter('tier:gold'), /档位/);
});

test('tier palette chips produce filters that match their tier', async () => {
  const { TIER_FILTER_OPTIONS } = await import('../src/tier.ts');
  assert.deepEqual(TIER_FILTER_OPTIONS.map((o) => [o.value, o.label]), [['premium', 'Premium'], ['premium_plus', 'Premium+']]);
  const legacy = { tier: undefined, months: 6, status: 'active', batch: '', username: '' };
  const plus = { tier: 'premium_plus', months: 12, status: 'active', batch: '', username: '' };
  for (const { value } of TIER_FILTER_OPTIONS) {
    const match = parseFilter(`tier:${value}`);
    assert.equal(match(legacy), value === 'premium');
    assert.equal(match(plus), value === 'premium_plus');
    assert.equal(parseFilter(`tier!=${value}`)(plus), value !== 'premium_plus');
  }
  assert.equal(parseFilter('tier:premium_plus and months:12')(plus), true);
});
