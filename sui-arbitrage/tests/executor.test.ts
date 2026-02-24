import { describe, it, expect, vi } from "vitest";
import BigNumber from "bignumber.js";
import type { ArbOpportunity, DexAdapter } from "../src/types.js";
import { DexName } from "../src/types.js";
import { ArbExecutor } from "../src/arbitrage/executor.js";

// Minimal mock SuiClientWrapper
function mockSuiClient() {
  return {
    client: {} as any,
    keypair: null,
    address: "0x1234",
    getBalance: vi.fn().mockResolvedValue(1_000_000_000n),
    executeTransaction: vi.fn().mockResolvedValue("mock-digest"),
    getObject: vi.fn(),
  } as any;
}

function mockAdapter(dex: DexName): DexAdapter {
  return {
    name: dex,
    getPools: vi.fn().mockResolvedValue([]),
    getQuote: vi.fn(),
    buildSwapTx: vi.fn().mockResolvedValue(new Uint8Array(100)),
  };
}

function makeOpp(overrides?: Partial<ArbOpportunity>): ArbOpportunity {
  return {
    id: "test-arb-1",
    type: "direct",
    tokenPath: [],
    legs: [
      {
        dex: DexName.Cetus,
        pool: { id: "p1", dex: DexName.Cetus, tokenA: {} as any, tokenB: {} as any, fee: 30, liquidity: new BigNumber(0), lastUpdate: Date.now() },
        tokenIn: { address: "0x2::sui::SUI", symbol: "SUI", decimals: 9 },
        tokenOut: { address: "0xusdc", symbol: "USDC", decimals: 6 },
        amountIn: new BigNumber(10),
        amountOut: new BigNumber(20),
      },
      {
        dex: DexName.Turbos,
        pool: { id: "p2", dex: DexName.Turbos, tokenA: {} as any, tokenB: {} as any, fee: 30, liquidity: new BigNumber(0), lastUpdate: Date.now() },
        tokenIn: { address: "0xusdc", symbol: "USDC", decimals: 6 },
        tokenOut: { address: "0x2::sui::SUI", symbol: "SUI", decimals: 9 },
        amountIn: new BigNumber(20),
        amountOut: new BigNumber(10.5),
      },
    ],
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

describe("ArbExecutor", () => {
  it("simulates execution in dry-run mode", async () => {
    const suiClient = mockSuiClient();
    const adapters = [mockAdapter(DexName.Cetus), mockAdapter(DexName.Turbos)];
    const executor = new ArbExecutor(suiClient, adapters, true);

    const result = await executor.execute(makeOpp());
    expect(result.success).toBe(true);
    expect(result.txDigest).toContain("dry-run");
    expect(executor.stats.totalSuccessful).toBe(1);
    expect(executor.stats.totalProfitSui.gt(0)).toBe(true);
  });

  it("rejects stale opportunities", async () => {
    const suiClient = mockSuiClient();
    const executor = new ArbExecutor(suiClient, [], true);

    const result = await executor.execute(
      makeOpp({ timestamp: Date.now() - 10_000 })
    );
    expect(result.success).toBe(false);
    expect(result.error).toBe("stale");
  });

  it("tracks execution stats correctly", async () => {
    const suiClient = mockSuiClient();
    const executor = new ArbExecutor(suiClient, [], true);

    await executor.execute(makeOpp({ id: "a", netProfit: new BigNumber(0.5) }));
    await executor.execute(makeOpp({ id: "b", netProfit: new BigNumber(0.3) }));

    expect(executor.stats.totalOpportunities).toBe(2);
    expect(executor.stats.totalSuccessful).toBe(2);
    expect(executor.stats.totalProfitSui.toFixed(1)).toBe("1.0");
  });
});
