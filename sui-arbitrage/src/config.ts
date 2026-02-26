import "dotenv/config";
import type { AppConfig } from "./types.js";

function env(key: string, fallback?: string): string {
  const val = process.env[key];
  if (val !== undefined) return val;
  if (fallback !== undefined) return fallback;
  throw new Error(`Missing required env var: ${key}`);
}

function envBool(key: string, fallback: boolean): boolean {
  const val = process.env[key];
  if (val === undefined) return fallback;
  return val.toLowerCase() === "true" || val === "1";
}

function envInt(key: string, fallback: number): number {
  const val = process.env[key];
  if (val === undefined) return fallback;
  const parsed = parseInt(val, 10);
  if (isNaN(parsed)) throw new Error(`Invalid integer for ${key}: ${val}`);
  return parsed;
}

export function loadConfig(): AppConfig {
  const network = env("SUI_NETWORK", "mainnet") as AppConfig["network"];
  if (!["mainnet", "testnet", "devnet"].includes(network)) {
    throw new Error(`Invalid SUI_NETWORK: ${network}`);
  }

  const config: AppConfig = {
    network,
    rpcUrl: env("SUI_RPC_URL", "https://fullnode.mainnet.sui.io:443"),
    privateKey: process.env["SUI_PRIVATE_KEY"],
    mnemonic: process.env["SUI_MNEMONIC"],
    dryRun: envBool("DRY_RUN", true),
    minProfitBps: envInt("MIN_PROFIT_BPS", 30),
    maxTradeSizeSui: envInt("MAX_TRADE_SIZE_SUI", 100),
    gasBudget: envInt("GAS_BUDGET", 50_000_000),
    scanIntervalMs: envInt("SCAN_INTERVAL_MS", 1000),
    logLevel: env("LOG_LEVEL", "info"),
    dexes: {
      cetus: envBool("CETUS_ENABLED", true),
      turbos: envBool("TURBOS_ENABLED", true),
      deepbook: envBool("DEEPBOOK_ENABLED", true),
    },
  };

  if (!config.privateKey && !config.mnemonic) {
    if (!config.dryRun) {
      throw new Error(
        "SUI_PRIVATE_KEY or SUI_MNEMONIC required when DRY_RUN=false"
      );
    }
  }

  return config;
}
