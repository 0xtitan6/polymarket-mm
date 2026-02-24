import BigNumber from "bignumber.js";
import { v4 as uuid } from "crypto";
import type { DexAdapter, ArbOpportunity, ArbLeg, Pool, Quote, Token } from "../types.js";
import { profitBps } from "../utils/math.js";
import { getLogger } from "../utils/logger.js";

interface DetectorConfig {
  minProfitBps: number;
  maxTradeSizeSui: number;
  gasEstimateSui: number;
}

/**
 * Scans quotes from multiple DEXes to find profitable arbitrage routes.
 *
 * Strategies:
 *   1. Direct:     Buy tokenA on DexX, sell tokenA on DexY
 *   2. Triangular: A→B on DexX, B→C on DexY, C→A on DexZ
 */
export class ArbDetector {
  private readonly adapters: DexAdapter[];
  private readonly config: DetectorConfig;

  constructor(adapters: DexAdapter[], config: DetectorConfig) {
    this.adapters = adapters;
    this.config = config;
  }

  // ── Direct arbitrage ──

  async findDirectOpportunities(
    tokenA: Token,
    tokenB: Token,
    inputAmount: BigNumber
  ): Promise<ArbOpportunity[]> {
    const log = getLogger();
    const opportunities: ArbOpportunity[] = [];

    // Gather quotes from every DEX for both directions
    const buyQuotes: Quote[] = []; // buy tokenB with tokenA
    const sellQuotes: Quote[] = []; // sell tokenB for tokenA

    for (const adapter of this.adapters) {
      const pools = await adapter.getPools(tokenA, tokenB);
      for (const pool of pools) {
        try {
          const buyQ = await adapter.getQuote(pool, tokenA, inputAmount);
          if (!buyQ.amountOut.isZero()) buyQuotes.push(buyQ);
        } catch (err) {
          log.debug({ err, dex: adapter.name, pool: pool.id }, "Quote failed");
        }

        try {
          // For sell side, we need quotes going the other direction
          const sellQ = await adapter.getQuote(pool, tokenB, inputAmount);
          if (!sellQ.amountOut.isZero()) sellQuotes.push(sellQ);
        } catch (err) {
          log.debug({ err, dex: adapter.name, pool: pool.id }, "Quote failed");
        }
      }
    }

    // Compare: buy on DEX_i, sell on DEX_j
    for (const buy of buyQuotes) {
      for (const sell of sellQuotes) {
        if (buy.dex === sell.dex && buy.pool.id === sell.pool.id) continue;

        // Route: spend inputAmount of tokenA → get tokenB on buy.dex
        //        spend that tokenB → get tokenA back on sell.dex
        const intermediateAmount = buy.amountOut;
        // Re-quote sell side with the exact intermediate amount
        const sellAdapter = this.adapters.find((a) => a.name === sell.dex);
        if (!sellAdapter) continue;

        let finalQuote: Quote;
        try {
          finalQuote = await sellAdapter.getQuote(
            sell.pool,
            tokenB,
            intermediateAmount
          );
        } catch {
          continue;
        }

        const outputAmount = finalQuote.amountOut;
        const bps = profitBps(inputAmount, outputAmount);
        const gasEstimate = new BigNumber(this.config.gasEstimateSui);
        const netProfit = outputAmount.minus(inputAmount).minus(gasEstimate);

        if (bps >= this.config.minProfitBps && netProfit.gt(0)) {
          const opp: ArbOpportunity = {
            id: crypto.randomUUID(),
            type: "direct",
            tokenPath: [tokenA, tokenB, tokenA],
            legs: [
              {
                dex: buy.dex,
                pool: buy.pool,
                tokenIn: tokenA,
                tokenOut: tokenB,
                amountIn: inputAmount,
                amountOut: intermediateAmount,
              },
              {
                dex: sell.dex,
                pool: sell.pool,
                tokenIn: tokenB,
                tokenOut: tokenA,
                amountIn: intermediateAmount,
                amountOut: outputAmount,
              },
            ],
            inputAmount,
            expectedOutput: outputAmount,
            expectedProfit: outputAmount.minus(inputAmount),
            profitBps: bps,
            gasEstimate,
            netProfit,
            timestamp: Date.now(),
          };

          opportunities.push(opp);
          log.info(
            {
              buyDex: buy.dex,
              sellDex: sell.dex,
              profitBps: bps,
              netProfit: netProfit.toFixed(4),
              pair: `${tokenA.symbol}/${tokenB.symbol}`,
            },
            "Direct arb found"
          );
        }
      }
    }

    // Sort by net profit descending
    opportunities.sort((a, b) => b.netProfit.comparedTo(a.netProfit));
    return opportunities;
  }

  // ── Triangular arbitrage ──

  async findTriangularOpportunities(
    tokenA: Token,
    tokenB: Token,
    tokenC: Token,
    inputAmount: BigNumber
  ): Promise<ArbOpportunity[]> {
    const log = getLogger();
    const opportunities: ArbOpportunity[] = [];

    // Leg 1: A → B
    const leg1Quotes = await this.getQuotesAcrossDexes(tokenA, tokenB, inputAmount);

    for (const q1 of leg1Quotes) {
      // Leg 2: B → C
      const leg2Quotes = await this.getQuotesAcrossDexes(tokenB, tokenC, q1.amountOut);

      for (const q2 of leg2Quotes) {
        // Leg 3: C → A
        const leg3Quotes = await this.getQuotesAcrossDexes(tokenC, tokenA, q2.amountOut);

        for (const q3 of leg3Quotes) {
          const outputAmount = q3.amountOut;
          const bps = profitBps(inputAmount, outputAmount);
          const gasEstimate = new BigNumber(this.config.gasEstimateSui).times(1.5); // 3 legs ≈ 1.5× gas
          const netProfit = outputAmount.minus(inputAmount).minus(gasEstimate);

          if (bps >= this.config.minProfitBps && netProfit.gt(0)) {
            const opp: ArbOpportunity = {
              id: crypto.randomUUID(),
              type: "triangular",
              tokenPath: [tokenA, tokenB, tokenC, tokenA],
              legs: [
                {
                  dex: q1.dex,
                  pool: q1.pool,
                  tokenIn: tokenA,
                  tokenOut: tokenB,
                  amountIn: inputAmount,
                  amountOut: q1.amountOut,
                },
                {
                  dex: q2.dex,
                  pool: q2.pool,
                  tokenIn: tokenB,
                  tokenOut: tokenC,
                  amountIn: q1.amountOut,
                  amountOut: q2.amountOut,
                },
                {
                  dex: q3.dex,
                  pool: q3.pool,
                  tokenIn: tokenC,
                  tokenOut: tokenA,
                  amountIn: q2.amountOut,
                  amountOut: outputAmount,
                },
              ],
              inputAmount,
              expectedOutput: outputAmount,
              expectedProfit: outputAmount.minus(inputAmount),
              profitBps: bps,
              gasEstimate,
              netProfit,
              timestamp: Date.now(),
            };

            opportunities.push(opp);
            log.info(
              {
                path: `${tokenA.symbol}→${tokenB.symbol}→${tokenC.symbol}→${tokenA.symbol}`,
                profitBps: bps,
                netProfit: netProfit.toFixed(4),
              },
              "Triangular arb found"
            );
          }
        }
      }
    }

    opportunities.sort((a, b) => b.netProfit.comparedTo(a.netProfit));
    return opportunities;
  }

  // ── Helpers ──

  private async getQuotesAcrossDexes(
    tokenIn: Token,
    tokenOut: Token,
    amountIn: BigNumber
  ): Promise<Quote[]> {
    const quotes: Quote[] = [];
    for (const adapter of this.adapters) {
      const pools = await adapter.getPools(tokenIn, tokenOut);
      for (const pool of pools) {
        try {
          const q = await adapter.getQuote(pool, tokenIn, amountIn);
          if (!q.amountOut.isZero()) quotes.push(q);
        } catch {
          // skip
        }
      }
    }
    return quotes;
  }
}
