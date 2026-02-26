import { describe, it, expect, vi } from "vitest";
import BigNumber from "bignumber.js";
import type { DexAdapter, Pool, Quote, Token } from "../src/types.js";
import { DexName } from "../src/types.js";
import { ArbDetector } from "../src/arbitrage/detector.js";

const SUI: Token = { address: "0x2::sui::SUI", symbol: "SUI", decimals: 9 };
const USDC: Token = { address: "0xusdc::usdc::USDC", symbol: "USDC", decimals: 6 };

function makePool(dex: DexName, id: string): Pool {
  return {
    id,
    dex,
    tokenA: SUI,
    tokenB: USDC,
    fee: 30,
    liquidity: new BigNumber(100_000),
    sqrtPrice: new BigNumber(1),
    lastUpdate: Date.now(),
  };
}

function makeMockAdapter(
  dex: DexName,
  pool: Pool,
  priceMultiplier: number
): DexAdapter {
  return {
    name: dex,
    getPools: vi.fn().mockResolvedValue([pool]),
    getQuote: vi.fn().mockImplementation(
      async (_pool: Pool, tokenIn: Token, amountIn: BigNumber): Promise<Quote> => {
        const isForward = tokenIn.address === SUI.address;
        const amountOut = isForward
          ? amountIn.times(priceMultiplier)
          : amountIn.div(priceMultiplier);
        return {
          dex,
          pool: _pool,
          tokenIn,
          tokenOut: isForward ? USDC : SUI,
          amountIn,
          amountOut,
          priceImpact: new BigNumber(0),
          fee: new BigNumber(0),
          timestamp: Date.now(),
        };
      }
    ),
    buildSwapTx: vi.fn().mockResolvedValue(new Uint8Array()),
  };
}

describe("ArbDetector", () => {
  it("detects a direct arb when prices diverge", async () => {
    const poolA = makePool(DexName.Cetus, "pool-cetus");
    const poolB = makePool(DexName.Turbos, "pool-turbos");

    // Cetus: 1 SUI = 2.0 USDC (cheap SUI)
    // Turbos: 1 SUI = 2.2 USDC (expensive SUI)
    const adapterA = makeMockAdapter(DexName.Cetus, poolA, 2.0);
    const adapterB = makeMockAdapter(DexName.Turbos, poolB, 2.2);

    const detector = new ArbDetector([adapterA, adapterB], {
      minProfitBps: 1,
      maxTradeSizeSui: 100,
      gasEstimateSui: 0.001,
    });

    const opps = await detector.findDirectOpportunities(
      SUI,
      USDC,
      new BigNumber(10)
    );

    // Should find at least one opportunity
    expect(opps.length).toBeGreaterThan(0);
    expect(opps[0].netProfit.gt(0)).toBe(true);
    expect(opps[0].type).toBe("direct");
  });

  it("returns empty when prices are equal", async () => {
    const poolA = makePool(DexName.Cetus, "pool-cetus");
    const poolB = makePool(DexName.Turbos, "pool-turbos");

    // Same price on both DEXes
    const adapterA = makeMockAdapter(DexName.Cetus, poolA, 2.0);
    const adapterB = makeMockAdapter(DexName.Turbos, poolB, 2.0);

    const detector = new ArbDetector([adapterA, adapterB], {
      minProfitBps: 10,
      maxTradeSizeSui: 100,
      gasEstimateSui: 0.01,
    });

    const opps = await detector.findDirectOpportunities(
      SUI,
      USDC,
      new BigNumber(10)
    );

    expect(opps.length).toBe(0);
  });

  it("detects a triangular arb", async () => {
    const WETH: Token = { address: "0xweth::coin::COIN", symbol: "WETH", decimals: 8 };

    const poolSuiUsdc = makePool(DexName.Cetus, "sui-usdc");
    const poolUsdcWeth: Pool = { ...makePool(DexName.Turbos, "usdc-weth"), tokenA: USDC, tokenB: WETH };
    const poolWethSui: Pool = { ...makePool(DexName.DeepBook, "weth-sui"), tokenA: WETH, tokenB: SUI };

    // Prices that create a triangular arb opportunity
    const cetus: DexAdapter = {
      name: DexName.Cetus,
      getPools: vi.fn().mockImplementation(async (tA: Token, tB: Token) => {
        if (tA.symbol === "SUI" && tB.symbol === "USDC") return [poolSuiUsdc];
        return [];
      }),
      getQuote: vi.fn().mockImplementation(async (_p: Pool, _tIn: Token, amt: BigNumber) => ({
        dex: DexName.Cetus, pool: _p, tokenIn: _tIn, tokenOut: USDC,
        amountIn: amt, amountOut: amt.times(2.0), priceImpact: new BigNumber(0),
        fee: new BigNumber(0), timestamp: Date.now(),
      })),
      buildSwapTx: vi.fn().mockResolvedValue(new Uint8Array()),
    };

    const turbos: DexAdapter = {
      name: DexName.Turbos,
      getPools: vi.fn().mockImplementation(async (tA: Token, tB: Token) => {
        if (tA.symbol === "USDC" && tB.symbol === "WETH") return [poolUsdcWeth];
        return [];
      }),
      getQuote: vi.fn().mockImplementation(async (_p: Pool, _tIn: Token, amt: BigNumber) => ({
        dex: DexName.Turbos, pool: _p, tokenIn: _tIn, tokenOut: WETH,
        amountIn: amt, amountOut: amt.times(0.6), priceImpact: new BigNumber(0),
        fee: new BigNumber(0), timestamp: Date.now(),
      })),
      buildSwapTx: vi.fn().mockResolvedValue(new Uint8Array()),
    };

    const deepbook: DexAdapter = {
      name: DexName.DeepBook,
      getPools: vi.fn().mockImplementation(async (tA: Token, tB: Token) => {
        if (tA.symbol === "WETH" && tB.symbol === "SUI") return [poolWethSui];
        return [];
      }),
      getQuote: vi.fn().mockImplementation(async (_p: Pool, _tIn: Token, amt: BigNumber) => ({
        dex: DexName.DeepBook, pool: _p, tokenIn: _tIn, tokenOut: SUI,
        amountIn: amt, amountOut: amt.times(2.0), priceImpact: new BigNumber(0),
        fee: new BigNumber(0), timestamp: Date.now(),
      })),
      buildSwapTx: vi.fn().mockResolvedValue(new Uint8Array()),
    };

    const detector = new ArbDetector([cetus, turbos, deepbook], {
      minProfitBps: 1,
      maxTradeSizeSui: 100,
      gasEstimateSui: 0.01,
    });

    // 10 SUI → 20 USDC → 12 WETH → 24 SUI = 140% profit
    const opps = await detector.findTriangularOpportunities(
      SUI,
      USDC,
      WETH,
      new BigNumber(10)
    );

    expect(opps.length).toBeGreaterThan(0);
    expect(opps[0].type).toBe("triangular");
    expect(opps[0].legs.length).toBe(3);
    expect(opps[0].netProfit.gt(0)).toBe(true);
  });
});
