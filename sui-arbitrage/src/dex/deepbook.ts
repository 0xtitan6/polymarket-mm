import BigNumber from "bignumber.js";
import { Transaction } from "@mysten/sui/transactions";
import type { SuiClient } from "@mysten/sui/client";
import type { DexAdapter, Pool, Quote, Token } from "../types.js";
import { DexName } from "../types.js";
import { toRawAmount, fromRawAmount } from "../utils/math.js";
import { getLogger } from "../utils/logger.js";

// DeepBook V3 package (mainnet)
const DEEPBOOK_PACKAGE =
  "0xdee9bc4ee1ce2aad154dd3a5d264bf7ca3d1daac8f7d2ec77e1e44a493145b3f";

interface DeepBookPoolInfo {
  poolId: string;
  baseCoin: string;
  quoteCoin: string;
  takerFee: number;
  makerRebate: number;
  bestBid: string;
  bestAsk: string;
  baseVolume: string;
}

export class DeepBookAdapter implements DexAdapter {
  readonly name = DexName.DeepBook;
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
      // Query DeepBook pools from on-chain registry
      const resp = await fetch(
        `https://deepbook-indexer.mainnet.mystenlabs.com/pools`
      );
      if (!resp.ok) {
        throw new Error(`DeepBook indexer returned ${resp.status}`);
      }
      const data = (await resp.json()) as DeepBookPoolInfo[];

      const matching = data.filter((p) => {
        const coins = [p.baseCoin, p.quoteCoin].sort();
        const target = [tokenA.address, tokenB.address].sort();
        return coins[0] === target[0] && coins[1] === target[1];
      });

      const pools: Pool[] = matching.map((p) => {
        const bestBid = new BigNumber(p.bestBid || "0");
        const bestAsk = new BigNumber(p.bestAsk || "0");
        const midPrice =
          bestBid.isZero() || bestAsk.isZero()
            ? new BigNumber(0)
            : bestBid.plus(bestAsk).div(2);

        return {
          id: p.poolId,
          dex: DexName.DeepBook,
          tokenA, // base
          tokenB, // quote
          fee: p.takerFee,
          liquidity: new BigNumber(p.baseVolume),
          sqrtPrice: midPrice.sqrt(),
          lastUpdate: Date.now(),
        };
      });

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
    const isBuy = tokenIn.address === pool.tokenB.address; // paying quote to get base
    const tokenOut = isBuy ? pool.tokenA : pool.tokenB;

    // For order book DEX, use mid price as estimate
    let estimatedOut: BigNumber;
    if (pool.sqrtPrice && !pool.sqrtPrice.isZero()) {
      const midPrice = pool.sqrtPrice.pow(2);
      const feeMultiplier = new BigNumber(10_000 - pool.fee).div(10_000);
      estimatedOut = isBuy
        ? amountIn.div(midPrice).times(feeMultiplier)
        : amountIn.times(midPrice).times(feeMultiplier);
    } else {
      estimatedOut = new BigNumber(0);
    }

    return {
      dex: DexName.DeepBook,
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
    const isBuy = tokenIn.address === pool.tokenB.address;
    const rawIn = toRawAmount(amountIn, tokenIn.decimals);
    const tokenOut = isBuy ? pool.tokenA : pool.tokenB;
    const rawMinOut = toRawAmount(minAmountOut, tokenOut.decimals);

    const tx = new Transaction();
    tx.setSender(sender);

    const [coinIn] = tx.splitCoins(tx.gas, [tx.pure.u64(rawIn)]);

    if (isBuy) {
      // Market buy: spend quote coin to get base coin
      tx.moveCall({
        target: `${DEEPBOOK_PACKAGE}::clob_v2::swap_exact_quote_for_base`,
        arguments: [
          tx.object(pool.id),
          tx.pure.u64(rawIn), // quote quantity
          tx.object("0x6"), // clock
          coinIn, // quote coin
        ],
        typeArguments: [pool.tokenA.address, pool.tokenB.address],
      });
    } else {
      // Market sell: spend base coin to get quote coin
      tx.moveCall({
        target: `${DEEPBOOK_PACKAGE}::clob_v2::swap_exact_base_for_quote`,
        arguments: [
          tx.object(pool.id),
          tx.pure.u64(rawIn), // base quantity
          coinIn, // base coin
          tx.pure.u64(rawMinOut), // min quote out
          tx.object("0x6"), // clock
        ],
        typeArguments: [pool.tokenA.address, pool.tokenB.address],
      });
    }

    return await tx.build({ client: this.client });
  }
}
