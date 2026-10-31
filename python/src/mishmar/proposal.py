"""Build and sign what an agent sends to POST /v1/proposals.

The engine authenticates a proposal by its signature alone: the canonical
JSON of the submitted object without ``signature``, signed with the agent's
key. The bytes are pinned in spec/vectors/proposal-signing.json, which the
tests here run.

Rules:

* size, price and leverage are decimal strings, signed byte for byte as
  written; no float ever touches them;
* proposedAt and expiresAt are RFC 3339 in UTC with a Z suffix, the fraction
  trimmed of trailing zeros and omitted when zero;
* optional fields (leverage, price) are present only when set, never null;
* a cancel (kind "cancel") carries target_client_order_id and none of the
  order fields; every other kind carries side, size, order_type and
  reduce_only;
* venue_message, when the signer built the venue request itself, is a nested
  object canonicalised the same way, keys sorted.
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field
from datetime import UTC, datetime, timedelta
from typing import Any

from ._time import parse_time as _parse_time
from ._time import rfc3339
from .agentkey import AgentKey, new_nonce
from .canon import canonical_json

__all__ = [
    "KINDS",
    "ORDER_TYPES",
    "SCHEMES",
    "SIDES",
    "Proposal",
    "VenueMessage",
    "is_decimal",
    "new_nonce",
    "rfc3339",
]

KINDS = ("order", "transaction", "signed_message", "cancel")
SCHEMES = ("frost-ed25519", "ecdsa-2p")
SIDES = ("buy", "sell")
ORDER_TYPES = ("limit", "market")

_DECIMAL = re.compile(r"^-?[0-9]+(\.[0-9]+)?$")


def is_decimal(s: Any) -> bool:
    """Whether s is a decimal string the contract accepts ("0.5", "-12", "98000.25")."""
    return isinstance(s, str) and _DECIMAL.match(s) is not None


@dataclass
class VenueMessage:
    """The venue request the agent's signer built, with its half of the
    signature, for the one-shot path. Mirrors the contract's
    ProposalVenueMessage; base64 throughout."""

    venue_key_id: str
    scheme: str
    message: str
    engine_commitment_id: str
    share: str
    commitment: str | None = None

    def validate(self) -> None:
        if not all(
            (
                self.venue_key_id,
                self.scheme,
                self.message,
                self.engine_commitment_id,
                self.share,
            )
        ):
            raise ValueError(
                "venue_message needs venue_key_id, scheme, message, engine_commitment_id and share"
            )
        if self.scheme not in SCHEMES:
            raise ValueError(f"venue_message.scheme must be one of {SCHEMES}")
        if self.scheme == "frost-ed25519" and not self.commitment:
            raise ValueError("a FROST venue_message needs the signer's commitment")

    def to_wire(self) -> dict[str, Any]:
        body: dict[str, Any] = {
            "venueKeyId": self.venue_key_id,
            "scheme": self.scheme,
            "message": self.message,
            "engineCommitmentId": self.engine_commitment_id,
            "share": self.share,
        }
        if self.commitment is not None:
            body["commitment"] = self.commitment
        return body

    @classmethod
    def from_wire(cls, body: dict[str, Any]) -> VenueMessage:
        return cls(
            venue_key_id=body["venueKeyId"],
            scheme=body["scheme"],
            message=body["message"],
            engine_commitment_id=body["engineCommitmentId"],
            share=body["share"],
            commitment=body.get("commitment"),
        )


@dataclass
class Proposal:
    """One proposal. Mirrors the contract's ProposalSubmit; construct it with
    the agent's intent and let ``sign`` fill the rest. A cancel names the
    order to pull and leaves side, size and price out:
    ``Proposal(trading_account_id, symbol, kind="cancel", target_client_order_id=...)``."""

    trading_account_id: str
    symbol: str
    side: str | None = None
    size: str | None = None
    order_type: str = "limit"
    price: str | None = None
    leverage: str | None = None
    reduce_only: bool = False
    kind: str = "order"
    target_client_order_id: str | None = None
    venue_message: VenueMessage | None = None
    key_id: str = ""
    nonce: str = field(default_factory=new_nonce)
    proposed_at: datetime | None = None
    expires_at: datetime | None = None
    valid_for: timedelta = timedelta(minutes=1)
    signature: str | None = None

    def validate(self) -> None:
        """Raise ValueError naming the first field the engine would refuse."""
        if not self.key_id:
            raise ValueError("key_id is required (the Client fills it in)")
        if not self.trading_account_id:
            raise ValueError("trading_account_id is required")
        if not self.symbol:
            raise ValueError("symbol is required")
        if not self.nonce:
            raise ValueError("nonce is required")
        if self.kind not in KINDS:
            raise ValueError(f"kind must be one of {KINDS}")
        if self.venue_message is not None:
            self.venue_message.validate()
        proposed, expires = self.times()
        if expires <= proposed:
            raise ValueError("expires_at must be after proposed_at")
        if self.kind == "cancel":
            if not self.target_client_order_id:
                raise ValueError(
                    "a cancel needs target_client_order_id, the clientOrderId of the order to pull"
                )
            if (
                self.side is not None
                or self.size is not None
                or self.price is not None
                or self.leverage is not None
                or self.reduce_only
            ):
                raise ValueError(
                    "a cancel carries no order fields (side, size, price, leverage, reduce_only)"
                )
            return
        if self.target_client_order_id is not None:
            raise ValueError("target_client_order_id is for cancels only")
        if self.side not in SIDES:
            raise ValueError(f"side must be one of {SIDES}")
        if self.order_type not in ORDER_TYPES:
            raise ValueError(f"order_type must be one of {ORDER_TYPES}")
        if not is_decimal(self.size):
            raise ValueError('size must be a decimal string such as "0.5"')
        if self.order_type == "limit" and not is_decimal(self.price):
            raise ValueError("a limit order needs a decimal price string")
        if self.order_type == "market" and self.price is not None:
            raise ValueError("a market order carries no price")
        if self.leverage is not None and not is_decimal(self.leverage):
            raise ValueError("leverage must be a decimal string")
        if not isinstance(self.reduce_only, bool):
            raise ValueError("reduce_only must be a bool")

    def times(self) -> tuple[datetime, datetime]:
        """The proposal's times, defaulting to now and now plus valid_for."""
        proposed = (
            _parse_time(self.proposed_at) if self.proposed_at else datetime.now(UTC)
        )
        expires = (
            _parse_time(self.expires_at)
            if self.expires_at
            else proposed + self.valid_for
        )
        return proposed, expires

    def to_wire(self) -> dict[str, Any]:
        """The request body as the contract names it (without the signature when unsigned)."""
        proposed, expires = self.times()
        body: dict[str, Any] = {
            "keyId": self.key_id,
            "kind": self.kind,
            "nonce": self.nonce,
            "proposedAt": rfc3339(proposed),
            "expiresAt": rfc3339(expires),
            "tradingAccountId": self.trading_account_id,
            "symbol": self.symbol,
        }
        if self.kind == "cancel":
            if self.target_client_order_id is not None:
                body["targetClientOrderId"] = self.target_client_order_id
        else:
            body["orderType"] = self.order_type
            body["reduceOnly"] = self.reduce_only
            body["side"] = self.side
            body["size"] = self.size
            if self.price is not None:
                body["price"] = self.price
            if self.leverage is not None:
                body["leverage"] = self.leverage
            if self.target_client_order_id is not None:
                body["targetClientOrderId"] = self.target_client_order_id
        if self.venue_message is not None:
            body["venueMessage"] = self.venue_message.to_wire()
        if self.signature is not None:
            body["signature"] = self.signature
        return body

    @classmethod
    def from_wire(cls, body: dict[str, Any]) -> Proposal:
        """The inverse of to_wire, for vectors and for replaying a stored proposal."""
        vm = body.get("venueMessage")
        return cls(
            key_id=body["keyId"],
            kind=body.get("kind", "order"),
            nonce=body["nonce"],
            order_type=body.get("orderType", "limit"),
            price=body.get("price"),
            leverage=body.get("leverage"),
            proposed_at=_parse_time(body["proposedAt"]),
            expires_at=_parse_time(body["expiresAt"]),
            reduce_only=body.get("reduceOnly", False),
            trading_account_id=body["tradingAccountId"],
            side=body.get("side"),
            size=body.get("size"),
            symbol=body["symbol"],
            target_client_order_id=body.get("targetClientOrderId"),
            venue_message=VenueMessage.from_wire(vm) if vm is not None else None,
            signature=body.get("signature"),
        )

    def signing_bytes(self) -> bytes:
        """What the agent signs: the canonical JSON of the proposal without the signature."""
        body = self.to_wire()
        body.pop("signature", None)
        return canonical_json(body)

    def sign(self, key: AgentKey) -> Proposal:
        """Pin the times, validate, and set the signature. Returns self."""
        proposed, expires = self.times()
        self.proposed_at, self.expires_at = proposed, expires
        self.validate()
        self.signature = key.sign(self.signing_bytes())
        return self
