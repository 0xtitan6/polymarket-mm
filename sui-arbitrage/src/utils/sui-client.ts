import { SuiClient, getFullnodeUrl } from "@mysten/sui/client";
import { Ed25519Keypair } from "@mysten/sui/keypairs/ed25519";
import { Transaction } from "@mysten/sui/transactions";
import { decodeSuiPrivateKey } from "@mysten/sui/cryptography";
import type { AppConfig } from "../types.js";
import { getLogger } from "./logger.js";

export class SuiClientWrapper {
  readonly client: SuiClient;
  readonly keypair: Ed25519Keypair | null;
  readonly address: string;
  private readonly config: AppConfig;

  constructor(config: AppConfig) {
    this.config = config;

    const url =
      config.rpcUrl || getFullnodeUrl(config.network);
    this.client = new SuiClient({ url });

    if (config.privateKey) {
      const { secretKey } = decodeSuiPrivateKey(config.privateKey);
      this.keypair = Ed25519Keypair.fromSecretKey(secretKey);
      this.address = this.keypair.getPublicKey().toSuiAddress();
    } else if (config.mnemonic) {
      this.keypair = Ed25519Keypair.deriveKeypair(config.mnemonic);
      this.address = this.keypair.getPublicKey().toSuiAddress();
    } else {
      this.keypair = null;
      this.address = "0x0000000000000000000000000000000000000000000000000000000000000000";
    }

    getLogger().info({ address: this.address, network: config.network }, "Sui client initialized");
  }

  async getBalance(coinType = "0x2::sui::SUI"): Promise<bigint> {
    const balance = await this.client.getBalance({
      owner: this.address,
      coinType,
    });
    return BigInt(balance.totalBalance);
  }

  async executeTransaction(txBytes: Uint8Array): Promise<string> {
    if (!this.keypair) {
      throw new Error("No keypair configured — cannot sign transactions");
    }

    if (this.config.dryRun) {
      getLogger().info("DRY RUN: would execute transaction");
      return "dry-run-digest";
    }

    const tx = Transaction.from(txBytes);
    const result = await this.client.signAndExecuteTransaction({
      signer: this.keypair,
      transaction: tx,
      options: { showEffects: true, showEvents: true },
    });

    if (result.effects?.status?.status !== "success") {
      throw new Error(
        `Transaction failed: ${result.effects?.status?.error ?? "unknown error"}`
      );
    }

    getLogger().info({ digest: result.digest }, "Transaction executed");
    return result.digest;
  }

  async getObject(objectId: string) {
    return this.client.getObject({
      id: objectId,
      options: { showContent: true, showType: true },
    });
  }
}
