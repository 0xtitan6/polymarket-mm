import { describe, it, expect } from "vitest";
import BigNumber from "bignumber.js";
import {
  findOptimalInputSize,
  scoreOpportunity,
  rankOpportunities,
} from "../src/arbitrage/calculator.js";
import type { ArbOpportunity } from "../src/types.js";
import { DexName } from "../src/types.js";

function makeOpp(overrides: Partial<ArbOpportunity>): ArbOpportunity {
  return {
    id: "test-opp",
    type: "direct",
    tokenPath: [],
    legs: [],
    inputAmount: new BigNumber(10),
    expectedOutput: new BigNumber(10.5),
    expectedProfit: new BigNumber(0.5),
    profitBps: 500,
    gasEstimate: new BigNumber(0.01),
    netProfit: new BigNumber(0.49),
    timestamp: Date.now(),
    ...overrides,
  };
}

describe("findOptimalInputSize", () => {
  it("finds the profit-maximizing input for a concave profit curve", () => {
    // Simulated AMM: output = input × 1.02 − 0.001 × input² (concave)
    const quoteFn = (input: BigNumber) =>
      input.times(1.02).minus(input.pow(2).times(0.001));

    const { optimalInput, expectedProfit } = findOptimalInputSize(
      quoteFn,
      new BigNumber(1),
      new BigNumber(20),
      new BigNumber(0.01),
      50
    );

    expect(optimalInput.gt(5)).toBe(true);
    expect(optimalInput.lt(15)).toBe(true);
    expect(expectedProfit.gt(0)).toBe(true);
  });

  it("returns min input when always unprofitable", () => {
    const quoteFn = (input: BigNumber) => input.times(0.9); // 10% loss always

    const { expectedProfit } = findOptimalInputSize(
      quoteFn,
      new BigNumber(1),
      new BigNumber(100),
      new BigNumber(0.5)
    );

    expect(expectedProfit.lt(0)).toBe(true);
  });
});

describe("scoreOpportunity", () => {
  it("scores profitable opportunities positively", () => {
    const opp = makeOpp({ netProfit: new BigNumber(1) });
    expect(scoreOpportunity(opp)).toBeGreaterThan(0);
  });

  it("prefers higher profit", () => {
    const small = makeOpp({ netProfit: new BigNumber(0.1), gasEstimate: new BigNumber(0.01) });
    const large = makeOpp({ netProfit: new BigNumber(1.0), gasEstimate: new BigNumber(0.01) });
    expect(scoreOpportunity(large)).toBeGreaterThan(scoreOpportunity(small));
  });

  it("penalizes stale opportunities", () => {
    const fresh = makeOpp({ timestamp: Date.now() });
    const stale = makeOpp({ timestamp: Date.now() - 4_000 });
    expect(scoreOpportunity(fresh)).toBeGreaterThan(scoreOpportunity(stale));
  });
});

describe("rankOpportunities", () => {
  it("filters out unprofitable opportunities", () => {
    const opps = [
      makeOpp({ id: "good", netProfit: new BigNumber(0.5) }),
      makeOpp({ id: "bad", netProfit: new BigNumber(-0.1) }),
    ];
    const ranked = rankOpportunities(opps);
    expect(ranked.length).toBe(1);
    expect(ranked[0].id).toBe("good");
  });

  it("limits to maxConcurrent", () => {
    const opps = Array.from({ length: 10 }, (_, i) =>
      makeOpp({ id: `opp-${i}`, netProfit: new BigNumber(i + 1) })
    );
    const ranked = rankOpportunities(opps, 3);
    expect(ranked.length).toBe(3);
  });

  it("returns empty for empty input", () => {
    expect(rankOpportunities([])).toEqual([]);
  });
});
