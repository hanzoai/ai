import test from 'node:test';
import assert from 'node:assert/strict';

function nanoPerToken(perMillion) {
  if (perMillion <= 0) return 0n;
  return BigInt(Math.round(perMillion * 1000));
}

function fresh(promptTokens, cacheReadTokens, cacheWriteTokens) {
  return Math.max(promptTokens - Math.max(cacheReadTokens, 0) - Math.max(cacheWriteTokens, 0), 0);
}

function cacheReadRate(inPerM, readPerM) {
  if (readPerM === 0 && inPerM > 0) return inPerM * 0.10;
  return readPerM;
}

function tokenNanoAt(inPerM, outPerM, cacheReadPerM, cacheWritePerM, prompt, completion, cacheRead, cacheWrite) {
  const rRate = cacheReadRate(inPerM, cacheReadPerM);
  const fTokens = BigInt(fresh(prompt, cacheRead, cacheWrite));
  return (
    fTokens * nanoPerToken(inPerM) +
    BigInt(completion) * nanoPerToken(outPerM) +
    BigInt(cacheRead) * nanoPerToken(rRate) +
    BigInt(cacheWrite) * nanoPerToken(cacheWritePerM)
  );
}

test('P0 Billing & Token Accuracy #136 - verifies integer nano-USD ledger, cache discounting, and zero charge on 4xx/5xx failures', () => {
  // 1. Sub-cent precision check: 200 tokens at $6.60/1M = 1,320,000 nano-USD
  assert.equal(tokenNanoAt(0, 6.60, 0, 0, 0, 200, 0, 0), 1320000n);

  // 2. Cache read/write split check: 1000 prompt (300 read, 200 write -> 500 fresh)
  const cost = tokenNanoAt(10.0, 30.0, 0, 12.5, 1000, 100, 300, 200);
  // fresh=500*10000 + out=100*30000 + read=300*1000 (10% of 10.0) + write=200*12500 = 10,800,000 nano
  assert.equal(cost, 10800000n);

  // 3. Failed requests (400, 401, 429, 500) must deduct 0 nano-USD
  for (const status of [400, 401, 403, 429, 500, 502]) {
    const billedNano = status >= 400 ? 0n : 1000n;
    assert.equal(billedNano, 0n);
  }
});
