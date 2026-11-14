/**
 * The shapes of an agent's reads of connected-account data (contract 0.21.0),
 * as the engine answers them. Money, sizes and prices are decimal strings,
 * never numbers; times are RFC 3339 strings in UTC. spec/openapi.yaml has
 * every field's full description.
 *
 * A connected account is one pool of money at a venue, with its own
 * balances, positions, transactions and value (`equityUsd`): a Binance login
 * has a Spot and a USDⓈ-M Futures account, a Hyperliquid address has Perps
 * and Spot. Its `connection` is the venue login that holds it, with
 * `accountCount`, how many accounts the login has: the accounts' values add
 * up to the login's only when the agent was given all of them. Every
 * balance, position and transaction belongs to exactly one account. A move between two
 * accounts of one connection is a `transfer_out` on one and a `transfer_in`
 * on the other, each naming the other in `counterpartConnectedAccountId`.
 */

/** observed: read from the venue at that moment. rebuilt: worked out afterwards for a moment nobody was reading the account; no engine rebuilds yet. */
export type SnapshotSource = "observed" | "rebuilt";

/** transfer_in and transfer_out cross the account's edge: deposits, withdrawals, and moves to or from another account. A move between two accounts of one connection is a transfer_out on one and a transfer_in on the other, each naming the other side (`counterpartConnectedAccountId`); it moves each account's value by the amount and the connection's by nothing. */
export type AccountTransactionKind = "trade" | "transfer_in" | "transfer_out" | "funding" | "fee" | "liquidation" | "rebate" | "other";

export type EquityResolution = "minute" | "hour" | "day";

/** What kind of market a symbol is on: a symbol names a market only with its kind (Binance's BTCUSDT is a spot pair and a perpetual). */
export type MarketKind = "perp" | "future" | "spot" | "option" | "equity";

/** spot: assets held outright. derivatives: the collateral behind perpetuals and futures, with their positions. margin: assets bought with borrowing. funding: deposits, withdrawals and payments, trading nothing. unified: one pool behind every kind of market. other: anything else the venue keeps apart. */
export type ConnectedAccountType = "spot" | "derivatives" | "margin" | "funding" | "unified" | "other";

/** Who an agent key belongs to (`GET /agent/me`). */
export interface AgentIdentity {
  agentId: string;
  agentName: string;
  accountId: string;
  keyId: string;
  portfolioId: string;
  /** What the agent may do; absent from an engine before contract 0.20.0, which means trade. */
  permission?: "read" | "trade";
}

/** How current one kind of a venue connection's data is, across its accounts: an account's `connection.sync`. */
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

/** Which login a connection is at the venue, in the venue's own terms. */
export interface VenueAccount {
  role: "main" | "subaccount";
  /** The venue's own identifier: a Hyperliquid address, a Binance UID, a sub-account's email or address. Absent until the engine has learnt it. */
  id?: string;
  /** `id` shortened for a line of text: `0x12ab…9f3c`, `UID …4471`. */
  label?: string;
  /** On a sub-account, its main account's `id` at the venue. */
  parentVenueAccountId?: string;
}

/** What an account says about its connection, the venue login that holds it. Nothing secret. */
export interface VenueConnectionRef {
  id: string;
  /** The person's own name for the connection, e.g. "Carry". */
  name: string;
  /** How many accounts the login has, whether or not the agent was given all of them. Fewer seen than this: their sum is not the login's value. 1: this account is the whole login. */
  accountCount: number;
  kind: "exchange" | "hyperliquid" | "cosigner_callback";
  venueAccount: VenueAccount;
  /** The connection's setup, without any secret. */
  setup: { kind: string } & Record<string, unknown>;
  /** On a sub-account's connection, the connection of its main login, when that is connected too. */
  parentId?: string;
  /** How current each kind of the connection's data is, across its accounts: how fresh this account's data is. Absent until the connection is verified. */
  sync?: SyncState[];
}

/**
 * One pool of money at a venue, as the agent may see it. A person names it as
 * the venue, `connection.name`, `name` and `connection.venueAccount.label`,
 * leaving `name` out when `connection.accountCount` is 1.
 */
export interface ConnectedAccount {
  id: string;
  venueConnectionId: string;
  connection: VenueConnectionRef;
  /** What the venue calls it: "Spot", "USDⓈ-M Futures", "Perps"; "Main" on a connection with a single account. */
  name: string;
  type: ConnectedAccountType;
  /** The engine's stable word for the pool: `spot`, `usdm-futures`, `perps`, `main`. Unique within the connection. */
  venueRef: string;
  venue: string;
  status: "pending" | "active" | "paused" | "killed";
  mode: "agents_only" | "shared";
  /** Whether any agent holds a trading key on the account; false, Mishmar only reads it. */
  traded: boolean;
  /** Whether an agent can be given a trading key there at all. Absent means true. */
  tradable?: boolean;
  /** With `tradable` false, one line for a person saying why, without the account's name ("Mishmar does not trade spot on Binance yet"). Absent when the account can be traded. */
  untradableReason?: string;
  /** The account's value at `equityAsOf`: its balances at their prices plus its positions' unrealised PnL. Absent until the first reading. */
  equityUsd?: string;
  equityAsOf?: string;
  /** Assets held at `equityAsOf` with no price, so `equityUsd` leaves them out. Absent when every asset was priced. */
  unpricedAssets?: string[];
  restrictions?: Record<string, unknown>[];
  maintenance?: Record<string, unknown>;
  createdAt: string;
  updatedAt: string;
}

/** One asset the account holds. */
export interface Balance {
  asset: string;
  amount: string;
  /** The price `usdValue` was worked out at. Absent with it. */
  priceUsd?: string;
  /** Absent when the engine has no price for the asset; the snapshot's `unpricedAssets` then names it. */
  usdValue?: string;
}

export interface BalanceSnapshot {
  connectedAccountId: string;
  asOf: string;
  source: SnapshotSource;
  balances: Balance[];
  /** The account's value at `asOf`: balances at their prices plus unrealised PnL. An asset with no price is left out and named in `unpricedAssets`. */
  equityUsd?: string;
  /** Assets held at `asOf` with no price. Absent when every asset was priced. */
  unpricedAssets?: string[];
}

/** The venue's own net position in one market (a symbol with its market kind), whoever placed the orders behind it. Derivatives only: an asset held outright is a balance. */
export interface AccountPosition {
  market: MarketKind;
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
  /** On a transfer, the connected account on the other side, when the engine can tell: always for a move within one connection. Absent on a deposit or a withdrawal. */
  counterpartConnectedAccountId?: string;
  /** With `symbol`. */
  market?: MarketKind;
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
  /** The account's value at that moment. */
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
