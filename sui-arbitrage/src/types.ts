import BigNumber from "bignumber.js";

// ── DEX identifiers ──

export enum DexName {
  Cetus = "cetus",
  Turbos = "turbos",
  DeepBook = "deepbook",
}

// ── Token & Pool ──

export interface Token {
  address: string; // Sui type address e.g. "0x2::sui::SUI"
  symbol: string;
  decimals: number;
}

export interface Pool {
  id: string; // on-chain object ID
  dex: DexName;
  tokenA: Token;
  tokenB: Token;
  fee: number; // basis points
  liquidity: BigNumber;
  sqrtPrice?: BigNumber; // CLMM pools
  lastUpdate: number; // epoch ms
}

// ── Price quotes ──

export interface Quote {
  dex: DexName;
  pool: Pool;
  tokenIn: Token;
  tokenOut: Token;
  amountIn: BigNumber;
  amountOut: BigNumber;
  priceImpact: BigNumber; // percent
  fee: BigNumber;
  timestamp: number;
}

// ── Arbitrage opportunity ──

export interface ArbOpportunity {
  id: string;
  type: "direct" | "triangular";
  tokenPath: Token[];
  legs: ArbLeg[];
  inputAmount: BigNumber;
  expectedOutput: BigNumber;
  expectedProfit: BigNumber;
  profitBps: number;
  gasEstimate: BigNumber;
  netProfit: BigNumber;
  timestamp: number;
}

export interface ArbLeg {
  dex: DexName;
  pool: Pool;
  tokenIn: Token;
  tokenOut: Token;
  amountIn: BigNumber;
  amountOut: BigNumber;
}

// ── Execution ──

export interface TradeResult {
  success: boolean;
  txDigest?: string;
  actualAmountOut?: BigNumber;
  actualProfit?: BigNumber;
  gasUsed?: BigNumber;
  error?: string;
  timestamp: number;
}

export interface ExecutionStats {
  totalOpportunities: number;
  totalExecuted: number;
  totalSuccessful: number;
  totalFailed: number;
  totalProfitSui: BigNumber;
  totalGasSpent: BigNumber;
  netProfitSui: BigNumber;
  startTime: number;
}

// ── Configuration ──

export interface AppConfig {
  network: "mainnet" | "testnet" | "devnet";
  rpcUrl: string;
  privateKey?: string;
  mnemonic?: string;
  dryRun: boolean;
  minProfitBps: number;
  maxTradeSizeSui: number;
  gasBudget: number;
  scanIntervalMs: number;
  logLevel: string;
  dexes: {
    cetus: boolean;
    turbos: boolean;
    deepbook: boolean;
  };
}

// ── DEX adapter interface ──

export interface DexAdapter {
  readonly name: DexName;

  /** Discover tradeable pools for a token pair. */
  getPools(tokenA: Token, tokenB: Token): Promise<Pool[]>;

  /** Get a swap quote for a given input amount. */
  getQuote(
    pool: Pool,
    tokenIn: Token,
    amountIn: BigNumber
  ): Promise<Quote>;

  /** Build a swap transaction (unsigned). */
  buildSwapTx(
    pool: Pool,
    tokenIn: Token,
    amountIn: BigNumber,
    minAmountOut: BigNumber,
    sender: string
  ): Promise<Uint8Array>;
}
