import BigNumber from "bignumber.js";

BigNumber.config({ DECIMAL_PLACES: 18, ROUNDING_MODE: BigNumber.ROUND_DOWN });

/** Convert a human-readable amount to on-chain integer representation. */
export function toRawAmount(amount: BigNumber, decimals: number): bigint {
  return BigInt(amount.times(new BigNumber(10).pow(decimals)).toFixed(0));
}

/** Convert an on-chain integer to human-readable decimal. */
export function fromRawAmount(raw: bigint, decimals: number): BigNumber {
  return new BigNumber(raw.toString()).div(new BigNumber(10).pow(decimals));
}

/** Calculate profit in basis points: (output - input) / input × 10000 */
export function profitBps(input: BigNumber, output: BigNumber): number {
  if (input.isZero()) return 0;
  return output.minus(input).div(input).times(10_000).toNumber();
}

/** Calculate price impact as a percentage. */
export function priceImpact(
  idealOutput: BigNumber,
  actualOutput: BigNumber
): BigNumber {
  if (idealOutput.isZero()) return new BigNumber(0);
  return idealOutput.minus(actualOutput).div(idealOutput).times(100);
}

/** Apply slippage tolerance: amount × (1 - slippageBps / 10000) */
export function applySlippage(
  amount: BigNumber,
  slippageBps: number
): BigNumber {
  return amount.times(new BigNumber(10_000 - slippageBps).div(10_000));
}

/**
 * Compute the optimal input amount for a constant-product AMM arb.
 *
 * Given reserves (Ra, Rb) and a target price p (external market),
 * the optimal trade size that moves the pool price to p is:
 *   amountIn = sqrt(Ra × Rb × p × (1-f)) - Ra
 * where f is the fee ratio.
 */
export function optimalAmmInput(
  reserveA: BigNumber,
  reserveB: BigNumber,
  targetPrice: BigNumber,
  feeBps: number
): BigNumber {
  const feeMultiplier = new BigNumber(10_000 - feeBps).div(10_000);
  const product = reserveA.times(reserveB).times(targetPrice).times(feeMultiplier);
  const sqrtProduct = product.sqrt();
  const optimal = sqrtProduct.minus(reserveA);
  return BigNumber.max(optimal, new BigNumber(0));
}
