"""The shapes of an agent's reads of connected-account data (contract 0.20.0).

What the engine answers is plain JSON, decoded into plain dicts; these typed
dicts give them their keys and types for a type checker, nothing at run
time. Keys are as on the wire (camelCase). Money, sizes and prices are
decimal strings, never floats; times are RFC 3339 strings in UTC.
``spec/openapi.yaml`` has every field's full description.
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
EquityResolution = Literal["minute", "hour", "day"]


class AgentIdentity(TypedDict):
    """Who an agent key belongs to (``GET /agent/me``)."""

    agentId: str
    agentName: str
    accountId: str
    keyId: str
    portfolioId: str


class SyncState(TypedDict):
    """How current one kind of a connected account's data is."""

    kind: Literal["balances", "positions", "equity", "transactions"]
    status: Literal["backfilling", "current", "behind", "failing"]
    asOf: NotRequired[str]
    historyFrom: NotRequired[str]
    lastReadAt: NotRequired[str]
    progress: NotRequired[str]
    error: NotRequired[str]


class ConnectedAccount(TypedDict):
    """One account at a venue, as the agent may see it: its setup carries no secret."""

    id: str
    name: str
    venue: str
    kind: Literal["exchange", "hyperliquid", "cosigner_callback"]
    status: Literal["pending", "active", "paused", "killed"]
    mode: Literal["agents_only", "shared"]
    #: Whether any agent holds a trading key on the account; false, Mishmar only reads it.
    traded: bool
    setup: dict[str, Any]
    createdAt: str
    updatedAt: str
    sync: NotRequired[list[SyncState]]
    verification: NotRequired[dict[str, Any]]
    restrictions: NotRequired[list[dict[str, Any]]]
    maintenance: NotRequired[dict[str, Any]]


class Balance(TypedDict):
    asset: str
    wallet: Literal["spot", "margin", "other"]
    amount: str
    #: Absent when the engine has no price for the asset.
    usdValue: NotRequired[str]


class BalanceSnapshot(TypedDict):
    connectedAccountId: str
    asOf: str
    source: SnapshotSource
    balances: list[Balance]
    #: The account's whole value, balances plus unrealised PnL.
    equityUsd: NotRequired[str]


class AccountPosition(TypedDict):
    """The venue's own net position in one symbol, whoever placed the orders behind it."""

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
    equityUsd: str
    source: SnapshotSource


class EquitySeries(TypedDict):
    connectedAccountId: str
    resolution: EquityResolution
    #: Oldest first.
    points: list[EquityPoint]
