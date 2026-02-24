import { describe, it, expect } from "vitest";
import BigNumber from "bignumber.js";
import {
  toRawAmount,
  fromRawAmount,
  profitBps,
  priceImpact,
  applySlippage,
  optimalAmmInput,
} from "../src/utils/math.js";

describe("toRawAmount", () => {
  it("converts human SUI to raw (9 decimals)", () => {
    const raw = toRawAmount(new BigNumber("1.5"), 9);
    expect(raw).toBe(1_500_000_000n);
  });

  it("converts human USDC to raw (6 decimals)", () => {
    const raw = toRawAmount(new BigNumber("100"), 6);
    expect(raw).toBe(100_000_000n);
  });

  it("handles zero", () => {
    expect(toRawAmount(new BigNumber(0), 9)).toBe(0n);
  });
});

describe("fromRawAmount", () => {
  it("converts raw to human SUI", () => {
    const human = fromRawAmount(1_500_000_000n, 9);
    expect(human.toFixed(1)).toBe("1.5");
  });

  it("converts raw to human USDC", () => {
    const human = fromRawAmount(100_000_000n, 6);
    expect(human.toFixed(0)).toBe("100");
  });
});

describe("profitBps", () => {
  it("calculates positive profit", () => {
    const bps = profitBps(new BigNumber(100), new BigNumber(103));
    expect(bps).toBe(300); // 3% = 300 bps
  });

  it("calculates negative profit", () => {
    const bps = profitBps(new BigNumber(100), new BigNumber(99));
    expect(bps).toBe(-100); // -1% = -100 bps
  });

  it("returns zero for equal amounts", () => {
    expect(profitBps(new BigNumber(50), new BigNumber(50))).toBe(0);
  });

  it("returns zero for zero input", () => {
    expect(profitBps(new BigNumber(0), new BigNumber(10))).toBe(0);
  });
});

describe("priceImpact", () => {
  it("calculates impact as percentage", () => {
    const impact = priceImpact(new BigNumber(100), new BigNumber(98));
    expect(impact.toFixed(0)).toBe("2"); // 2% impact
  });

  it("returns zero for equal values", () => {
    const impact = priceImpact(new BigNumber(100), new BigNumber(100));
    expect(impact.toNumber()).toBe(0);
  });
});

describe("applySlippage", () => {
  it("reduces amount by slippage bps", () => {
    // 50 bps = 0.5%
    const result = applySlippage(new BigNumber(1000), 50);
    expect(result.toFixed(1)).toBe("995.0");
  });

  it("returns full amount for zero slippage", () => {
    const result = applySlippage(new BigNumber(1000), 0);
    expect(result.toFixed(0)).toBe("1000");
  });
});

describe("optimalAmmInput", () => {
  it("returns positive for profitable arb", () => {
    const reserveA = new BigNumber(10_000);
    const reserveB = new BigNumber(10_000);
    const targetPrice = new BigNumber(1.05); // 5% above pool price
    const result = optimalAmmInput(reserveA, reserveB, targetPrice, 30);
    expect(result.gt(0)).toBe(true);
  });

  it("returns zero when pool price matches target", () => {
    const reserveA = new BigNumber(10_000);
    const reserveB = new BigNumber(10_000);
    const targetPrice = new BigNumber(1); // matches pool
    const result = optimalAmmInput(reserveA, reserveB, targetPrice, 30);
    // Should be approximately zero (rounding)
    expect(result.lt(1)).toBe(true);
  });
});
