"""The shapes of an agent's reads of connected-account data (contract 0.21.0).

What the engine answers is plain JSON, decoded into plain dicts; these typed
dicts give them their keys and types for a type checker, nothing at run
time. Keys are as on the wire (camelCase). Money, sizes and prices are
decimal strings, never floats; times are RFC 3339 strings in UTC.
``spec/openapi.yaml`` has every field's full description.

A connected account is one pool of money at a venue, with its own balances,
positions, transactions and value (``equityUsd``): a Binance login has a Spot
and a USDⓈ-M Futures account, a Hyperliquid address has Perps and Spot. Its
``connection`` is the venue login that holds it, with ``accountCount``, how
many accounts the login has: the accounts' values add up to the login's only
when the agent was given all of them. Every balance, position and
transaction belongs to exactly one account. A move between two accounts of one connection is a
``transfer_out`` on one and a ``transfer_in`` on the other, each naming the
other in ``counterpartConnectedAccountId``.
"""

from __future__ import annotations

from typing import Any, Literal, NotRequired, TypedDict

SnapshotSource = Literal["observed", "rebuilt"]
"""observed: read from the venue at that moment. rebuilt: worked out afterwards
for a moment nobody was reading the account; no engine rebuilds yet."""

AccountTransactionKind = Literal[
    "trade",
    "transfer_in",
    "transfer_out",
    "funding",
    "fee",
    "liquidation",
    "rebate",
    "other",
]
"""transfer_in and transfer_out cross the account's edge: deposits,
withdrawals, and moves to or from another account. A move between two
accounts of one connection is a transfer_out on one and a transfer_in on the
other, each naming the other side (``counterpartConnectedAccountId``); it
moves each account's value by the amount and the connection's by nothing."""

EquityResolution = Literal["minute", "hour", "day"]

MarketKind = Literal["perp", "future", "spot", "option", "equity"]
"""What kind of market a symbol is on: a symbol names a market only with its
kind (Binance's BTCUSDT is a spot pair and a perpetual)."""

ConnectedAccountType = Literal[
    "spot", "derivatives", "margin", "funding", "unified", "other"
]
"""spot: assets held outright. derivatives: the collateral behind perpetuals
and futures, with their positions. margin: assets bought with borrowing.
funding: deposits, withdrawals and payments, trading nothing. unified: one
pool behind every kind of market. other: anything else the venue keeps
apart."""


class AgentIdentity(TypedDict):
    """Who an agent key belongs to (``GET /agent/me``)."""

    agentId: str
    agentName: str
    accountId: str
    keyId: str
    portfolioId: str
    #: What the agent may do; absent from an engine before contract 0.20.0, which means trade.
    permission: NotRequired[Literal["read", "trade"]]


class SyncState(TypedDict):
    """How current one kind of a venue connection's data is, across its
    accounts: an account's ``connection["sync"]``."""

    kind: Literal["balances", "positions", "equity", "transactions"]
    status: Literal["backfilling", "current", "behind", "failing"]
    asOf: NotRequired[str]
    historyFrom: NotRequired[str]
    lastReadAt: NotRequired[str]
    progress: NotRequired[str]
    error: NotRequired[str]


class VenueAccount(TypedDict):
    """Which login a connection is at the venue, in the venue's own terms."""

    role: Literal["main", "subaccount"]
    #: The venue's own identifier: a Hyperliquid address, a Binance UID, a
    #: sub-account's email or address. Absent until the engine has learnt it.
    id: NotRequired[str]
    #: ``id`` shortened for a line of text: ``0x12ab…9f3c``, ``UID …4471``.
    label: NotRequired[str]
    #: On a sub-account, its main account's ``id`` at the venue.
    parentVenueAccountId: NotRequired[str]


class VenueConnectionRef(TypedDict):
    """What an account says about its connection, the venue login that holds
    it. Nothing secret."""

    id: str
    #: The person's own name for the connection, e.g. "Carry".
    name: str
    #: How many accounts the login has, whether or not the agent was given all
    #: of them. Fewer seen than this: their sum is not the login's value. 1:
    #: this account is the whole login.
    accountCount: int
    kind: Literal["exchange", "hyperliquid", "cosigner_callback"]
    venueAccount: VenueAccount
    #: The connection's setup, without any secret.
    setup: dict[str, Any]
    #: On a sub-account's connection, the connection of its main login, when
    #: that is connected too.
    parentId: NotRequired[str]
    #: How current each kind of the connection's data is, across its accounts:
    #: how fresh this account's data is. Absent until the connection is verified.
    sync: NotRequired[list[SyncState]]


class ConnectedAccount(TypedDict):
    """One pool of money at a venue, as the agent may see it. A person names
    it as venue, ``connection["name"]``, ``name`` and
    ``connection["venueAccount"]["label"]``, leaving ``name`` out when
    ``connection["accountCount"]`` is 1."""

    id: str
    venueConnectionId: str
    connection: VenueConnectionRef
    #: What the venue calls it: "Spot", "USDⓈ-M Futures", "Perps"; "Main" on a
    #: connection with a single account.
    name: str
    type: ConnectedAccountType
    #: The engine's stable word for the pool: ``spot``, ``usdm-futures``,
    #: ``perps``, ``main``. Unique within the connection.
    venueRef: str
    venue: str
    status: Literal["pending", "active", "paused", "killed"]
    mode: Literal["agents_only", "shared"]
    #: Whether any agent holds a trading key on the account; false, Mishmar only reads it.
    traded: bool
    createdAt: str
    updatedAt: str
    #: Whether an agent can be given a trading key there at all. Absent means true.
    tradable: NotRequired[bool]
    #: With ``tradable`` false, one line for a person saying why, without the
    #: account's name ("Mishmar does not trade spot on Binance yet"). Absent
    #: when the account can be traded.
    untradableReason: NotRequired[str]
    #: The account's value at ``equityAsOf``: its balances at their prices plus
    #: its positions' unrealised PnL. Absent until the first reading.
    equityUsd: NotRequired[str]
    equityAsOf: NotRequired[str]
    #: Assets held at ``equityAsOf`` with no price, so ``equityUsd`` leaves
    #: them out. Absent when every asset was priced.
    unpricedAssets: NotRequired[list[str]]
    restrictions: NotRequired[list[dict[str, Any]]]
    maintenance: NotRequired[dict[str, Any]]


class Balance(TypedDict):
    """One asset the account holds."""

    asset: str
    amount: str
    #: The price ``usdValue`` was worked out at. Absent with it.
    priceUsd: NotRequired[str]
    #: Absent when the engine has no price for the asset; the snapshot's
    #: ``unpricedAssets`` then names it.
    usdValue: NotRequired[str]


class BalanceSnapshot(TypedDict):
    connectedAccountId: str
    asOf: str
    source: SnapshotSource
    balances: list[Balance]
    #: The account's value at ``asOf``: balances at their prices plus
    #: unrealised PnL. An asset with no price is left out and named in
    #: ``unpricedAssets``.
    equityUsd: NotRequired[str]
    #: Assets held at ``asOf`` with no price. Absent when every asset was priced.
    unpricedAssets: NotRequired[list[str]]


class AccountPosition(TypedDict):
    """The venue's own net position in one market (a symbol with its market
    kind), whoever placed the orders behind it. Derivatives only: an asset
    held outright is a balance."""

    market: MarketKind
    symbol: str
    side: Literal["long", "short"]
    size: str
    entryPrice: str
    markPrice: NotRequired[str]
    notionalUsd: NotRequired[str]
    unrealizedUsd: NotRequired[str]
    leverage: NotRequired[str]


class AccountPositionSnapshot(TypedDict):
    connectedAccountId: str
    asOf: str
    source: SnapshotSource
    positions: list[AccountPosition]


class TransactionOrigin(TypedDict):
    """Who placed a trade: an agent through Mishmar, or something outside it."""

    kind: Literal["agent", "outside"]
    agentId: NotRequired[str]
    agentName: NotRequired[str]
    #: The decision that allowed it.
    decisionId: NotRequired[str]
    #: True when the calling agent placed it.
    own: NotRequired[bool]


class AccountTransaction(TypedDict):
    """One thing that happened on a connected account, as the venue reports it."""

    id: str
    connectedAccountId: str
    kind: AccountTransactionKind
    at: str
    venueId: NotRequired[str]
    #: On a transfer, the connected account on the other side, when the engine
    #: can tell: always for a move within one connection. Absent on a deposit
    #: or a withdrawal.
    counterpartConnectedAccountId: NotRequired[str]
    #: With ``symbol``.
    market: NotRequired[MarketKind]
    symbol: NotRequired[str]
    side: NotRequired[Literal["buy", "sell"]]
    size: NotRequired[str]
    price: NotRequired[str]
    #: What ``amount`` is in.
    asset: NotRequired[str]
    #: What the account gained (positive) or lost (negative), in ``asset``.
    amount: NotRequired[str]
    amountUsd: NotRequired[str]
    #: A trade's fee.
    feeUsd: NotRequired[str]
    #: Trades and liquidations; absent on the rest.
    origin: NotRequired[TransactionOrigin]


class AccountTransactionPage(TypedDict):
    items: list[AccountTransaction]
    #: Pass it as ``cursor`` for the page after; absent on the last.
    nextCursor: NotRequired[str]


class EquityPoint(TypedDict):
    at: str
    #: The account's value at that moment.
    equityUsd: str
    source: SnapshotSource


class EquitySeries(TypedDict):
    connectedAccountId: str
    resolution: EquityResolution
    #: Oldest first.
    points: list[EquityPoint]
