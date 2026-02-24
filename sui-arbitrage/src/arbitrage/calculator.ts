import BigNumber from "bignumber.js";
import type { ArbOpportunity, Quote } from "../types.js";
import { profitBps, optimalAmmInput } from "../utils/math.js";

/**
 * Compute the optimal input size for a direct arb between two AMM pools.
 *
 * For constant-product pools, there's an analytical optimum:
 *   optimal_input = sqrt(Ra1 × Rb1 × Ra2 × Rb2 × (1-f1)(1-f2)) / (Ra1×(1-f2) + Rb2) − Ra1
 *
 * For CLMM pools, we approximate with binary search over discrete quote calls.
 */
export function findOptimalInputSize(
  quoteFn: (amount: BigNumber) => BigNumber, // simplified: returns output for a given input
  minInput: BigNumber,
  maxInput: BigNumber,
  gasEstimate: BigNumber,
  steps = 20
): { optimalInput: BigNumber; expectedProfit: BigNumber } {
  let bestInput = minInput;
  let bestProfit = new BigNumber(-Infinity);

  const step = maxInput.minus(minInput).div(steps);

  for (let i = 0; i <= steps; i++) {
    const input = minInput.plus(step.times(i));
    const output = quoteFn(input);
    const profit = output.minus(input).minus(gasEstimate);

    if (profit.gt(bestProfit)) {
      bestProfit = profit;
      bestInput = input;
    }
  }

  // Refine around best with tighter binary search
  let lo = BigNumber.max(minInput, bestInput.minus(step));
  let hi = BigNumber.min(maxInput, bestInput.plus(step));

  for (let i = 0; i < 10; i++) {
    const mid1 = lo.plus(hi.minus(lo).div(3));
    const mid2 = hi.minus(hi.minus(lo).div(3));

    const profit1 = quoteFn(mid1).minus(mid1).minus(gasEstimate);
    const profit2 = quoteFn(mid2).minus(mid2).minus(gasEstimate);

    if (profit1.lt(profit2)) {
      lo = mid1;
    } else {
      hi = mid2;
    }
  }

  const optimal = lo.plus(hi).div(2);
  const optimalProfit = quoteFn(optimal).minus(optimal).minus(gasEstimate);

  return {
    optimalInput: optimal,
    expectedProfit: optimalProfit,
  };
}

/**
 * Score an opportunity for execution priority.
 *
 * Higher score = execute first.
 * Factors: net profit, profit per gas unit, recency, route simplicity.
 */
export function scoreOpportunity(opp: ArbOpportunity): number {
  const profitWeight = 0.5;
  const efficiencyWeight = 0.3;
  const freshnessWeight = 0.2;

  // Normalize profit (log scale to dampen large outliers)
  const profitScore = Math.log1p(Math.max(opp.netProfit.toNumber(), 0));

  // Gas efficiency: profit per unit gas
  const efficiency = opp.gasEstimate.isZero()
    ? 0
    : opp.netProfit.div(opp.gasEstimate).toNumber();
  const efficiencyScore = Math.log1p(Math.max(efficiency, 0));

  // Freshness: prefer newer opportunities (decay over 5 seconds)
  const ageMs = Date.now() - opp.timestamp;
  const freshnessScore = Math.max(0, 1 - ageMs / 5_000);

  return (
    profitWeight * profitScore +
    efficiencyWeight * efficiencyScore +
    freshnessWeight * freshnessScore
  );
}

/**
 * Rank and filter a set of opportunities for execution.
 */
export function rankOpportunities(
  opps: ArbOpportunity[],
  maxConcurrent = 3
): ArbOpportunity[] {
  return opps
    .filter((o) => o.netProfit.gt(0))
    .sort((a, b) => scoreOpportunity(b) - scoreOpportunity(a))
    .slice(0, maxConcurrent);
}
