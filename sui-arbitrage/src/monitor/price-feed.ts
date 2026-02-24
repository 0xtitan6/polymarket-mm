import BigNumber from "bignumber.js";
import type { DexAdapter, Pool, Token, Quote } from "../types.js";
import { getLogger } from "../utils/logger.js";

export interface PriceSnapshot {
  tokenA: Token;
  tokenB: Token;
  prices: Map<string, BigNumber>; // dex:poolId → price
  spread: BigNumber; // max price - min price
  spreadBps: number;
  timestamp: number;
}

/**
 * Monitors prices across all DEXes for a given token pair.
 * Emits snapshots that the detector uses to identify opportunities.
 */
export class PriceFeed {
  private readonly adapters: DexAdapter[];
  private readonly referenceAmount: BigNumber;
  private snapshots: Map<string, PriceSnapshot> = new Map();

  constructor(adapters: DexAdapter[], referenceAmountSui = 10) {
    this.adapters = adapters;
    this.referenceAmount = new BigNumber(referenceAmountSui);
  }

  async fetchPrices(tokenA: Token, tokenB: Token): Promise<PriceSnapshot> {
    const log = getLogger();
    const prices = new Map<string, BigNumber>();

    for (const adapter of this.adapters) {
      const pools = await adapter.getPools(tokenA, tokenB);
      for (const pool of pools) {
        try {
          const quote = await adapter.getQuote(pool, tokenA, this.referenceAmount);
          if (!quote.amountOut.isZero()) {
            const effectivePrice = quote.amountOut.div(quote.amountIn);
            prices.set(`${adapter.name}:${pool.id}`, effectivePrice);
          }
        } catch (err) {
          log.debug(
            { err, dex: adapter.name, pool: pool.id },
            "Price fetch failed"
          );
        }
      }
    }

    const priceValues = Array.from(prices.values());
    let spread = new BigNumber(0);
    let spreadBps = 0;

    if (priceValues.length >= 2) {
      const maxPrice = BigNumber.max(...priceValues);
      const minPrice = BigNumber.min(...priceValues);
      spread = maxPrice.minus(minPrice);
      if (!minPrice.isZero()) {
        spreadBps = spread.div(minPrice).times(10_000).toNumber();
      }
    }

    const snapshot: PriceSnapshot = {
      tokenA,
      tokenB,
      prices,
      spread,
      spreadBps,
      timestamp: Date.now(),
    };

    const pairKey = [tokenA.address, tokenB.address].sort().join("|");
    this.snapshots.set(pairKey, snapshot);

    if (spreadBps > 0) {
      log.debug(
        {
          pair: `${tokenA.symbol}/${tokenB.symbol}`,
          dexes: prices.size,
          spreadBps: spreadBps.toFixed(1),
        },
        "Price snapshot"
      );
    }

    return snapshot;
  }

  getLatest(tokenA: Token, tokenB: Token): PriceSnapshot | undefined {
    const pairKey = [tokenA.address, tokenB.address].sort().join("|");
    return this.snapshots.get(pairKey);
  }

  /** Pairs where spread exceeds a threshold — potential arb targets. */
  getWideSpreadPairs(minSpreadBps: number): PriceSnapshot[] {
    return Array.from(this.snapshots.values()).filter(
      (s) => s.spreadBps >= minSpreadBps
    );
  }
}
