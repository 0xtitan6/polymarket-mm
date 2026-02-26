import BigNumber from "bignumber.js";
import type { DexAdapter, Pool, Token } from "../types.js";
import { COMMON_PAIRS } from "../dex/base.js";
import { getLogger } from "../utils/logger.js";

export interface PoolIndex {
  pool: Pool;
  tokenA: Token;
  tokenB: Token;
}

/**
 * Discovers and indexes all available pools across DEXes.
 * Runs on startup and periodically refreshes to detect new pools.
 */
export class PoolScanner {
  private readonly adapters: DexAdapter[];
  private pools: PoolIndex[] = [];
  private lastScan = 0;

  constructor(adapters: DexAdapter[]) {
    this.adapters = adapters;
  }

  async scan(pairs?: [Token, Token][]): Promise<PoolIndex[]> {
    const log = getLogger();
    const targetPairs = pairs ?? COMMON_PAIRS;
    const discovered: PoolIndex[] = [];

    log.info(
      { pairs: targetPairs.length, dexes: this.adapters.length },
      "Scanning for pools"
    );

    for (const [tokenA, tokenB] of targetPairs) {
      for (const adapter of this.adapters) {
        try {
          const pools = await adapter.getPools(tokenA, tokenB);
          for (const pool of pools) {
            discovered.push({ pool, tokenA, tokenB });
          }
          if (pools.length > 0) {
            log.debug(
              {
                dex: adapter.name,
                pair: `${tokenA.symbol}/${tokenB.symbol}`,
                count: pools.length,
              },
              "Pools found"
            );
          }
        } catch (err) {
          log.error(
            { err, dex: adapter.name, pair: `${tokenA.symbol}/${tokenB.symbol}` },
            "Pool scan failed"
          );
        }
      }
    }

    this.pools = discovered;
    this.lastScan = Date.now();

    log.info(
      { totalPools: discovered.length },
      "Pool scan complete"
    );

    return discovered;
  }

  /** Return pools that have the same token pair across different DEXes (arb candidates). */
  getArbCandidates(): Map<string, PoolIndex[]> {
    const byPair = new Map<string, PoolIndex[]>();

    for (const entry of this.pools) {
      const pairKey = [entry.tokenA.address, entry.tokenB.address].sort().join("|");
      const list = byPair.get(pairKey) ?? [];
      list.push(entry);
      byPair.set(pairKey, list);
    }

    // Only keep pairs present on 2+ DEXes
    const candidates = new Map<string, PoolIndex[]>();
    for (const [key, entries] of byPair) {
      const dexes = new Set(entries.map((e) => e.pool.dex));
      if (dexes.size >= 2) {
        candidates.set(key, entries);
      }
    }

    return candidates;
  }

  /** Get pairs suitable for triangular arb (share intermediate tokens). */
  getTriangularCandidates(): [Token, Token, Token][] {
    const tokenIndex = new Map<string, Set<string>>(); // tokenAddr → set of paired token addrs

    for (const entry of this.pools) {
      const aKey = entry.tokenA.address;
      const bKey = entry.tokenB.address;
      if (!tokenIndex.has(aKey)) tokenIndex.set(aKey, new Set());
      if (!tokenIndex.has(bKey)) tokenIndex.set(bKey, new Set());
      tokenIndex.get(aKey)!.add(bKey);
      tokenIndex.get(bKey)!.add(aKey);
    }

    const triangles: [Token, Token, Token][] = [];
    const tokenMap = new Map<string, Token>();
    for (const entry of this.pools) {
      tokenMap.set(entry.tokenA.address, entry.tokenA);
      tokenMap.set(entry.tokenB.address, entry.tokenB);
    }

    const addrs = Array.from(tokenIndex.keys());
    for (let i = 0; i < addrs.length; i++) {
      for (let j = i + 1; j < addrs.length; j++) {
        if (!tokenIndex.get(addrs[i])!.has(addrs[j])) continue;
        // A-B exists, find C where B-C and C-A exist
        for (let k = j + 1; k < addrs.length; k++) {
          if (
            tokenIndex.get(addrs[j])!.has(addrs[k]) &&
            tokenIndex.get(addrs[k])!.has(addrs[i])
          ) {
            const a = tokenMap.get(addrs[i]);
            const b = tokenMap.get(addrs[j]);
            const c = tokenMap.get(addrs[k]);
            if (a && b && c) triangles.push([a, b, c]);
          }
        }
      }
    }

    return triangles;
  }

  get poolCount(): number {
    return this.pools.length;
  }

  get lastScanTime(): number {
    return this.lastScan;
  }
}
