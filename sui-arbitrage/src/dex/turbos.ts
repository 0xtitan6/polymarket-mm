import BigNumber from "bignumber.js";
import { Transaction } from "@mysten/sui/transactions";
import type { SuiClient } from "@mysten/sui/client";
import type { DexAdapter, Pool, Quote, Token } from "../types.js";
import { DexName } from "../types.js";
import { toRawAmount, fromRawAmount } from "../utils/math.js";
import { getLogger } from "../utils/logger.js";

// Turbos Finance package (mainnet)
const TURBOS_PACKAGE =
  "0x91bfbc386a41afcfd9b2533058d7e915a1d3829089cc268ff4333d54d6339ca1";
const TURBOS_VERSIONED =
  "0xf1cf0e81048df168ebeb1b8571f4b577f3f54a642c2f7f3a1adc33434f1b163b";

interface TurbosPoolData {
  objectId: string;
  coinTypeA: string;
  coinTypeB: string;
  fee: number;
  sqrtPrice: string;
  liquidity: string;
}

export class TurbosAdapter implements DexAdapter {
  readonly name = DexName.Turbos;
  private poolCache: Map<string, Pool[]> = new Map();
  private readonly client: SuiClient;

  constructor(client: SuiClient) {
    this.client = client;
  }

  async getPools(tokenA: Token, tokenB: Token): Promise<Pool[]> {
    const cacheKey = [tokenA.address, tokenB.address].sort().join("|");
    const cached = this.poolCache.get(cacheKey);
    if (cached && Date.now() - cached[0].lastUpdate < 30_000) {
      return cached;
    }

    try {
      // Query Turbos pool factory for matching pools
      const resp = await fetch(
        `https://api.turbos.finance/pools?coinTypeA=${tokenA.address}&coinTypeB=${tokenB.address}`
      );
      if (!resp.ok) {
        throw new Error(`Turbos API returned ${resp.status}`);
      }
      const data = (await resp.json()) as TurbosPoolData[];

      const pools: Pool[] = data.map((p) => ({
        id: p.objectId,
        dex: DexName.Turbos,
        tokenA,
        tokenB,
        fee: p.fee,
        liquidity: new BigNumber(p.liquidity),
        sqrtPrice: new BigNumber(p.sqrtPrice),
        lastUpdate: Date.now(),
      }));

      this.poolCache.set(cacheKey, pools);
      return pools;
    } catch (err) {
      getLogger().error({ err, dex: this.name }, "Failed to fetch pools");
      return [];
    }
  }

  async getQuote(
    pool: Pool,
    tokenIn: Token,
    amountIn: BigNumber
  ): Promise<Quote> {
    const isAtoB = tokenIn.address === pool.tokenA.address;
    const tokenOut = isAtoB ? pool.tokenB : pool.tokenA;

    // Estimate output using sqrt price (CLMM model)
    let estimatedOut: BigNumber;
    if (pool.sqrtPrice && !pool.sqrtPrice.isZero()) {
      const price = pool.sqrtPrice.div(new BigNumber(2).pow(64)).pow(2);
      const feeMultiplier = new BigNumber(10_000 - pool.fee).div(10_000);
      estimatedOut = isAtoB
        ? amountIn.times(price).times(feeMultiplier)
        : amountIn.div(price).times(feeMultiplier);
    } else {
      estimatedOut = new BigNumber(0);
    }

    return {
      dex: DexName.Turbos,
      pool,
      tokenIn,
      tokenOut,
      amountIn,
      amountOut: estimatedOut,
      priceImpact: new BigNumber(0),
      fee: amountIn.times(pool.fee).div(10_000),
      timestamp: Date.now(),
    };
  }

  async buildSwapTx(
    pool: Pool,
    tokenIn: Token,
    amountIn: BigNumber,
    minAmountOut: BigNumber,
    sender: string
  ): Promise<Uint8Array> {
    const isAtoB = tokenIn.address === pool.tokenA.address;
    const rawIn = toRawAmount(amountIn, tokenIn.decimals);
    const tokenOut = isAtoB ? pool.tokenB : pool.tokenA;
    const rawMinOut = toRawAmount(minAmountOut, tokenOut.decimals);

    const tx = new Transaction();
    tx.setSender(sender);

    const [coinIn] = tx.splitCoins(tx.gas, [tx.pure.u64(rawIn)]);
    tx.moveCall({
      target: `${TURBOS_PACKAGE}::swap_router::swap_a_b`,
      arguments: [
        tx.object(pool.id),
        coinIn,
        tx.pure.u64(rawIn),
        tx.pure.u64(rawMinOut),
        tx.pure.u128(isAtoB ? 4295048016n : 79226673515401279992447579055n),
        tx.pure.bool(true), // by_amount_in
        tx.object(sender),
        tx.pure.u64(Math.floor(Date.now() / 1000) + 600), // deadline
        tx.object("0x6"), // clock
        tx.object(TURBOS_VERSIONED),
      ],
      typeArguments: [pool.tokenA.address, pool.tokenB.address, `${TURBOS_PACKAGE}::pool::Fee`],
    });

    return await tx.build({ client: this.client });
  }
}
