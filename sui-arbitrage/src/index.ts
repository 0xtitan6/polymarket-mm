import BigNumber from "bignumber.js";
import { loadConfig } from "./config.js";
import { DexName } from "./types.js";
import type { AppConfig, DexAdapter, Token } from "./types.js";
import { initLogger, getLogger } from "./utils/logger.js";
import { SuiClientWrapper } from "./utils/sui-client.js";
import { DexRegistry, COMMON_PAIRS } from "./dex/base.js";
import { CetusAdapter } from "./dex/cetus.js";
import { TurbosAdapter } from "./dex/turbos.js";
import { DeepBookAdapter } from "./dex/deepbook.js";
import { PoolScanner } from "./monitor/pool-scanner.js";
import { PriceFeed } from "./monitor/price-feed.js";
import { ArbDetector } from "./arbitrage/detector.js";
import { ArbExecutor } from "./arbitrage/executor.js";
import { rankOpportunities } from "./arbitrage/calculator.js";

class SuiArbitrageBot {
  private config: AppConfig;
  private suiClient: SuiClientWrapper;
  private registry: DexRegistry;
  private scanner: PoolScanner;
  private priceFeed: PriceFeed;
  private detector: ArbDetector;
  private executor: ArbExecutor;
  private running = false;

  constructor(config: AppConfig) {
    this.config = config;
    this.suiClient = new SuiClientWrapper(config);
    this.registry = new DexRegistry();

    // Register enabled DEX adapters
    if (config.dexes.cetus) {
      this.registry.register(new CetusAdapter(this.suiClient.client));
    }
    if (config.dexes.turbos) {
      this.registry.register(new TurbosAdapter(this.suiClient.client));
    }
    if (config.dexes.deepbook) {
      this.registry.register(new DeepBookAdapter(this.suiClient.client));
    }

    const adapters = this.registry.all();
    this.scanner = new PoolScanner(adapters);
    this.priceFeed = new PriceFeed(adapters);
    this.detector = new ArbDetector(adapters, {
      minProfitBps: config.minProfitBps,
      maxTradeSizeSui: config.maxTradeSizeSui,
      gasEstimateSui: 0.01, // ~0.01 SUI per tx
    });
    this.executor = new ArbExecutor(this.suiClient, adapters, config.dryRun);
  }

  async start(): Promise<void> {
    const log = getLogger();
    this.running = true;

    log.info("╔══════════════════════════════════════════╗");
    log.info("║        Sui Arbitrage Bot v0.1.0          ║");
    log.info("╚══════════════════════════════════════════╝");
    log.info({
      network: this.config.network,
      dryRun: this.config.dryRun,
      dexes: this.registry.names(),
      address: this.suiClient.address,
      minProfitBps: this.config.minProfitBps,
      scanInterval: `${this.config.scanIntervalMs}ms`,
    }, "Bot configuration");

    if (this.config.dryRun) {
      log.warn("Running in DRY RUN mode — no real trades will be placed");
    }

    // Initial pool discovery
    log.info("Discovering pools...");
    await this.scanner.scan();
    log.info({ pools: this.scanner.poolCount }, "Pool discovery complete");

    // Main loop
    log.info("Starting arbitrage scan loop");
    await this.loop();
  }

  private async loop(): Promise<void> {
    const log = getLogger();
    let cycle = 0;

    while (this.running) {
      cycle++;
      const cycleStart = Date.now();

      try {
        // Refresh pools periodically (every 60 cycles)
        if (cycle % 60 === 0) {
          await this.scanner.scan();
        }

        // Fetch prices across all DEXes for all common pairs
        const arbCandidates = this.scanner.getArbCandidates();
        const allOpps = [];

        for (const [_pairKey, poolEntries] of arbCandidates) {
          const tokenA = poolEntries[0].tokenA;
          const tokenB = poolEntries[0].tokenB;
          const inputAmount = new BigNumber(this.config.maxTradeSizeSui);

          // Fetch price snapshot
          await this.priceFeed.fetchPrices(tokenA, tokenB);

          // Look for direct arb
          const directOpps = await this.detector.findDirectOpportunities(
            tokenA,
            tokenB,
            inputAmount
          );
          allOpps.push(...directOpps);
        }

        // Look for triangular arbs
        const triangles = this.scanner.getTriangularCandidates();
        for (const [tokenA, tokenB, tokenC] of triangles.slice(0, 5)) {
          const inputAmount = new BigNumber(this.config.maxTradeSizeSui);
          const triOpps = await this.detector.findTriangularOpportunities(
            tokenA,
            tokenB,
            tokenC,
            inputAmount
          );
          allOpps.push(...triOpps);
        }

        // Rank and execute the best opportunities
        if (allOpps.length > 0) {
          const ranked = rankOpportunities(allOpps);
          for (const opp of ranked) {
            await this.executor.execute(opp);
          }
        }

        // Stats every 30 cycles
        if (cycle % 30 === 0) {
          this.executor.printStats();
        }
      } catch (err) {
        log.error({ err }, "Error in scan cycle");
      }

      // Wait for next scan interval
      const elapsed = Date.now() - cycleStart;
      const wait = Math.max(0, this.config.scanIntervalMs - elapsed);
      if (wait > 0) {
        await sleep(wait);
      }
    }
  }

  stop(): void {
    this.running = false;
    getLogger().info("Shutting down...");
    this.executor.printStats();
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// ── Entry point ──

async function main(): Promise<void> {
  const config = loadConfig();
  const log = initLogger(config.logLevel);

  const bot = new SuiArbitrageBot(config);

  // Graceful shutdown
  const shutdown = () => {
    bot.stop();
    process.exit(0);
  };
  process.on("SIGINT", shutdown);
  process.on("SIGTERM", shutdown);

  try {
    await bot.start();
  } catch (err) {
    log.fatal({ err }, "Fatal error");
    process.exit(1);
  }
}

main();
