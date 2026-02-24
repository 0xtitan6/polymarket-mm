import BigNumber from "bignumber.js";
import { Transaction } from "@mysten/sui/transactions";
import type { SuiClient } from "@mysten/sui/client";
import type { DexAdapter, Pool, Quote, Token } from "../types.js";
import { DexName } from "../types.js";
import { fromRawAmount, toRawAmount } from "../utils/math.js";
import { getLogger } from "../utils/logger.js";

// Cetus CLMM package and global config (mainnet)
const CETUS_CLMM_PACKAGE =
  "0x1eabed72c53feb3805120a081dc15963c204dc8d091542592abaf7a35689b2fb";
const CETUS_GLOBAL_CONFIG =
  "0xdaa46292632c3c4d8f31f23ea0f9b36a28ff3677e9684980e4438403a67a3d8f";
const CETUS_POOLS_ENDPOINT =
  "https://api-sui.cetus.zone/v2/sui/pools_info";

interface CetusPoolInfo {
  pool_address: string;
  coin_a: { address: string; symbol: string; decimals: number };
  coin_b: { address: string; symbol: string; decimals: number };
  fee_rate: number;
  current_sqrt_price: string;
  liquidity: string;
}

export class CetusAdapter implements DexAdapter {
  readonly name = DexName.Cetus;
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
      const resp = await fetch(CETUS_POOLS_ENDPOINT);
      if (!resp.ok) {
        throw new Error(`Cetus API returned ${resp.status}`);
      }
      const data = (await resp.json()) as { data: { lp_list: CetusPoolInfo[] } };
      const pools = data.data.lp_list;

      const matching = pools
        .filter((p) => {
          const coins = [p.coin_a.address, p.coin_b.address].sort();
          const target = [tokenA.address, tokenB.address].sort();
          return coins[0] === target[0] && coins[1] === target[1];
        })
        .map((p): Pool => ({
          id: p.pool_address,
          dex: DexName.Cetus,
          tokenA: {
            address: p.coin_a.address,
            symbol: p.coin_a.symbol,
            decimals: p.coin_a.decimals,
          },
          tokenB: {
            address: p.coin_b.address,
            symbol: p.coin_b.symbol,
            decimals: p.coin_b.decimals,
          },
          fee: Math.round(p.fee_rate / 100), // convert to bps
          liquidity: new BigNumber(p.liquidity),
          sqrtPrice: new BigNumber(p.current_sqrt_price),
          lastUpdate: Date.now(),
        }));

      this.poolCache.set(cacheKey, matching);
      return matching;
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

    // Use Cetus on-chain simulation via dry-run
    const rawIn = toRawAmount(amountIn, tokenIn.decimals);

    // Estimate output using sqrt price for CLMM
    let estimatedOut: BigNumber;
    if (pool.sqrtPrice && !pool.sqrtPrice.isZero()) {
      const price = pool.sqrtPrice.div(new BigNumber(2).pow(64)).pow(2);
      const feeMultiplier = new BigNumber(10_000 - pool.fee).div(10_000);
      if (isAtoB) {
        estimatedOut = amountIn.times(price).times(feeMultiplier);
      } else {
        estimatedOut = amountIn.div(price).times(feeMultiplier);
      }
    } else {
      estimatedOut = new BigNumber(0);
    }

    return {
      dex: DexName.Cetus,
      pool,
      tokenIn,
      tokenOut,
      amountIn,
      amountOut: estimatedOut,
      priceImpact: new BigNumber(0), // computed post-execution
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

    // Cetus CLMM swap call
    const [coinIn] = tx.splitCoins(tx.gas, [tx.pure.u64(rawIn)]);
    tx.moveCall({
      target: `${CETUS_CLMM_PACKAGE}::pool_script::swap`,
      arguments: [
        tx.object(CETUS_GLOBAL_CONFIG),
        tx.object(pool.id),
        coinIn,
        tx.pure.bool(isAtoB),
        tx.pure.bool(true), // by_amount_in
        tx.pure.u64(rawIn),
        tx.pure.u64(rawMinOut),
        tx.pure.u128(isAtoB ? 4295048016n : 79226673515401279992447579055n), // sqrt price limit
        tx.object("0x6"), // clock
      ],
      typeArguments: [pool.tokenA.address, pool.tokenB.address],
    });

    const bytes = await tx.build({ client: this.client });
    return bytes;
  }
}
