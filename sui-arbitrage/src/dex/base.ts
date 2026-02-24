import type { DexAdapter, Pool, Token } from "../types.js";

/**
 * Registry of all enabled DEX adapters.
 * The arbitrage engine iterates these to fetch quotes and build swap TXs.
 */
export class DexRegistry {
  private adapters: Map<string, DexAdapter> = new Map();

  register(adapter: DexAdapter): void {
    this.adapters.set(adapter.name, adapter);
  }

  get(name: string): DexAdapter | undefined {
    return this.adapters.get(name);
  }

  all(): DexAdapter[] {
    return Array.from(this.adapters.values());
  }

  names(): string[] {
    return Array.from(this.adapters.keys());
  }
}

// ── Well-known Sui tokens ──

export const SUI: Token = {
  address: "0x2::sui::SUI",
  symbol: "SUI",
  decimals: 9,
};

export const USDC: Token = {
  address: "0xdba34672e30cb065b1f93e3ab55318768fd6fef66c15942c9f7cb846e2f900e7::usdc::USDC",
  symbol: "USDC",
  decimals: 6,
};

export const USDT: Token = {
  address: "0xc060006111016b8a020ad5b33834984a437aaa7d3c74c18e09a95d48aceab08c::coin::COIN",
  symbol: "USDT",
  decimals: 6,
};

export const WETH: Token = {
  address: "0xaf8cd5edc19c4512f4259f0bee101a40d41ebed738ade5874359610ef8eeced5::coin::COIN",
  symbol: "WETH",
  decimals: 8,
};

export const CETUS_TOKEN: Token = {
  address: "0x06864a6f921804860930db6ddbe2e16acdf8504495ea7481637a1c8b9a8fe54b::cetus::CETUS",
  symbol: "CETUS",
  decimals: 9,
};

/** Commonly traded pairs on Sui DEXes. */
export const COMMON_PAIRS: [Token, Token][] = [
  [SUI, USDC],
  [SUI, USDT],
  [USDC, USDT],
  [WETH, USDC],
  [CETUS_TOKEN, SUI],
];
