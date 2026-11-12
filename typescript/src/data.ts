/**
 * The shapes of an agent's reads of connected-account data (contract 0.20.0),
 * as the engine answers them. Money, sizes and prices are decimal strings,
 * never numbers; times are RFC 3339 strings in UTC. spec/openapi.yaml has
 * every field's full description.
 */

/** observed: read from the venue at that moment. rebuilt: worked out afterwards for a moment nobody was reading the account; no engine rebuilds yet. */
export type SnapshotSource = "observed" | "rebuilt";

export type AccountTransactionKind = "trade" | "transfer_in" | "transfer_out" | "funding" | "fee" | "liquidation" | "rebate" | "other";

export type EquityResolution = "minute" | "hour" | "day";

/** Who an agent key belongs to (`GET /agent/me`). */
export interface AgentIdentity {
  agentId: string;
  agentName: string;
  accountId: string;
  keyId: string;
  portfolioId: string;
}

/** How current one kind of a connected account's data is. */
export interface SyncState {
  kind: "balances" | "positions" | "equity" | "transactions";
  status: "backfilling" | "current" | "behind" | "failing";
  asOf?: string;
  historyFrom?: string;
  lastReadAt?: string;
  /** While backfilling, the share done, 0 to 1. */
  progress?: string;
  /** While failing, one line for a person. */
  error?: string;
}

/** One account at a venue, as the agent may see it: its setup carries no secret. */
export interface ConnectedAccount {
  id: string;
  name: string;
  venue: string;
  kind: "exchange" | "hyperliquid" | "cosigner_callback";
  status: "pending" | "active" | "paused" | "killed";
  mode: "agents_only" | "shared";
  /** Whether any agent holds a trading key on the account; false, Mishmar only reads it. */
  traded: boolean;
  setup: { kind: string } & Record<string, unknown>;
  sync?: SyncState[];
  verification?: Record<string, unknown>;
  restrictions?: Record<string, unknown>[];
  maintenance?: Record<string, unknown>;
  createdAt: string;
  updatedAt: string;
}

export interface Balance {
  asset: string;
  wallet: "spot" | "margin" | "other";
  amount: string;
  /** Absent when the engine has no price for the asset. */
  usdValue?: string;
}

export interface BalanceSnapshot {
  connectedAccountId: string;
  asOf: string;
  source: SnapshotSource;
  balances: Balance[];
  /** The account's whole value, balances plus unrealised PnL. */
  equityUsd?: string;
}

/** The venue's own net position in one symbol, whoever placed the orders behind it. */
export interface AccountPosition {
  symbol: string;
  side: "long" | "short";
  size: string;
  entryPrice: string;
  markPrice?: string;
  notionalUsd?: string;
  unrealizedUsd?: string;
  leverage?: string;
}

export interface AccountPositionSnapshot {
  connectedAccountId: string;
  asOf: string;
  source: SnapshotSource;
  positions: AccountPosition[];
}

/** Who placed a trade: an agent through Mishmar, or something outside it. */
export interface TransactionOrigin {
  kind: "agent" | "outside";
  agentId?: string;
  agentName?: string;
  /** The decision that allowed it. */
  decisionId?: string;
  /** True when the calling agent placed it. */
  own?: boolean;
}

/** One thing that happened on a connected account, as the venue reports it. */
export interface AccountTransaction {
  id: string;
  connectedAccountId: string;
  kind: AccountTransactionKind;
  at: string;
  venueId?: string;
  symbol?: string;
  side?: "buy" | "sell";
  size?: string;
  price?: string;
  /** What `amount` is in. */
  asset?: string;
  /** What the account gained (positive) or lost (negative), in `asset`. */
  amount?: string;
  amountUsd?: string;
  /** A trade's fee. */
  feeUsd?: string;
  /** Trades and liquidations; absent on the rest. */
  origin?: TransactionOrigin;
}

export interface AccountTransactionPage {
  items: AccountTransaction[];
  /** Pass it as `cursor` for the page after; absent on the last. */
  nextCursor?: string;
}

export interface EquityPoint {
  at: string;
  equityUsd: string;
  source: SnapshotSource;
}

export interface EquitySeries {
  connectedAccountId: string;
  resolution: EquityResolution;
  /** Oldest first. */
  points: EquityPoint[];
}

/** A time for a read: an RFC 3339 string as given, or a Date. */
export type When = string | Date;

/** The range and filters of a page of transactions. */
export interface TransactionQuery {
  from?: When;
  to?: When;
  kind?: AccountTransactionKind;
  /** The page's `nextCursor`, for the page after. */
  cursor?: string;
  /** 1 to 500; the engine's default is 100. */
  limit?: number;
}

/** The range and spacing of an equity series. */
export interface EquityQuery {
  from?: When;
  to?: When;
  /** Absent, the engine picks one that gives a few hundred points over the range. */
  resolution?: EquityResolution;
}
