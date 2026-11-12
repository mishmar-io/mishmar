"""A small HTTP client for the engine, on the standard library only.

Proposals go to POST /proposals with the signature in the body. Everything
else an agent may call (its own decisions, approvals, positions, PnL, the
connected accounts it was given and their data, and the signer channel) is
under /agent and signed with the agentAuth headers, so a bot can function
without an operator token and cannot act on the account.
"""

from __future__ import annotations

import json
import os
import random
import time
import urllib.error
import urllib.parse
import urllib.request
from collections.abc import Callable
from dataclasses import dataclass
from datetime import datetime
from email.utils import parsedate_to_datetime
from typing import Any

from ._time import rfc3339
from .agentkey import AgentKey, ParsedSecret, parse_secret
from .data import (
    AccountPositionSnapshot,
    AccountTransactionKind,
    AccountTransactionPage,
    AgentIdentity,
    BalanceSnapshot,
    ConnectedAccount,
    EquityResolution,
    EquitySeries,
)
from .proposal import Proposal

RESOLVED_APPROVAL = ("approved", "rejected", "expired")

#: The answers that mean the request did nothing and may go again: 503 while
#: the engine restarts, 429 when this caller sent too many.
RETRYABLE = (503, 429)


class EngineError(Exception):
    """The engine answered with an error: ``status`` (HTTP), ``code`` and ``message`` from its body."""

    def __init__(self, status: int, code: str, message: str):
        super().__init__(f"{status} {code}: {message}")
        self.status, self.code, self.message = status, code, message
        #: The ``Retry-After`` header of a 429, when the engine sent one.
        self.retry_after: str | None = None


@dataclass(frozen=True)
class Retries:
    """How a call the engine did nothing with goes again: 503 ``unavailable``
    while the engine restarts (its drain is 28 s) and 429 ``rate_limited``.
    The same request is sent again after the seconds in ``Retry-After`` when
    the engine names them, otherwise after a wait that doubles from
    ``min_wait`` to ``max_wait`` with jitter, until ``budget`` seconds have
    passed since the first attempt; then the last answer is raised. Nothing
    else is retried: a 413 ``too_large`` would come back the same, and any
    other answer means the request was taken. The defaults cover a restart.
    """

    budget: float = 45.0
    min_wait: float = 0.25
    max_wait: float = 8.0

    def wait(
        self, attempt: int, status: int, retry_after: str | None, elapsed: float
    ) -> float | None:
        """Seconds to wait before attempt ``attempt + 1`` (from 0), or ``None``
        to stop and raise the answer."""
        if status not in RETRYABLE:
            return None
        wait = _parse_retry_after(retry_after)
        if wait is None:
            step = min(self.min_wait * (2 ** min(attempt, 20)), self.max_wait)
            wait = step / 2 + random.uniform(0, step / 2)
        if elapsed + wait > self.budget:
            return None
        return wait


def _parse_retry_after(value: str | None) -> float | None:
    if not value:
        return None
    try:
        secs = int(value)
        return float(secs) if secs >= 0 else None
    except ValueError:
        pass
    try:
        when = parsedate_to_datetime(value)
    except (TypeError, ValueError):
        return None
    return max(when.timestamp() - time.time(), 0.0)


#: The default policy; see :class:`Retries`.
DEFAULT_RETRIES = Retries()

#: The agent's API key from the console (``msk_…``), which names its engine and key id.
ENV_API_KEY = "MISHMAR_API_KEY"
#: The engine's API base URL, with /v1, for a self-hosted engine or a key that names none.
ENV_ENGINE = "MISHMAR_ENGINE"

#: The API host of each hosted environment a key can name; an account's
#: engine is reached under ``/a/<account id>/v1`` on it.
HOSTED_API = {
    "testnet": "https://api.testnet.mishmar.io",
    "stage": "https://api.stage.mishmar.io",
    "prod": "https://api.mishmar.io",
}


def engine_url_for(parsed: ParsedSecret, override: str | None = None) -> str:
    """The API base URL a key's engine is reached at: ``override`` when
    given (trailing slashes dropped), else the hosted host the key's
    environment names, plus the account's path. A key that names no engine
    (issued before 0.19.0), or an environment that is not hosted (a
    self-hosted engine's), needs the override."""
    if override and override.strip():
        return override.strip().rstrip("/")
    if parsed.key_id is None or parsed.account_id is None:
        raise ValueError(
            f"the key was issued before 0.19.0 and names no engine; pass the engine's API base URL, with /v1, or set {ENV_ENGINE}"
        )
    host = HOSTED_API.get(parsed.environment or "")
    if host is None:
        raise ValueError(
            f"the key names environment {parsed.environment!r}, which is not a hosted one; pass the engine's API base URL, with /v1, or set {ENV_ENGINE}"
        )
    return f"{host}/a/{parsed.account_id}/v1"


def _when(value: str | datetime | None) -> str | None:
    """A time for a query: a datetime in the contract's form, a string as given."""
    if value is None or isinstance(value, str):
        return value
    return rfc3339(value)


class Client:
    """Talks to one engine as one agent. The trading key's share lives in the
    agent's signer, never here: this client proposes, reads, and relays the
    signer channel.

    ``retries`` is how a call the engine did nothing with goes again
    (:class:`Retries`, on by default); ``None`` turns retries off."""

    def __init__(
        self,
        engine_url: str,
        agent_key: AgentKey,
        key_id: str,
        *,
        timeout: float = 10.0,
        retries: Retries | None = DEFAULT_RETRIES,
    ):
        self.engine_url = engine_url.rstrip("/")
        self.agent_key = agent_key
        self.key_id = key_id
        self.timeout = timeout
        self.retries = retries
        self._sleep: Callable[[float], None] = time.sleep

    @classmethod
    def from_api_key(
        cls,
        api_key: str | None = None,
        *,
        engine_url: str | None = None,
        timeout: float = 10.0,
        retries: Retries | None = DEFAULT_RETRIES,
    ) -> Client:
        """A client from the agent's Mishmar API key alone (default
        ``$MISHMAR_API_KEY``): the key names its engine and its key id.
        ``engine_url`` (default ``$MISHMAR_ENGINE``) is the engine's API base
        URL, with /v1, for a self-hosted engine. A read agent needs nothing
        else; a key issued before 0.19.0 names neither, so it takes
        ``Client(engine_url, AgentKey.parse(secret), key_id)``."""
        key = api_key if api_key is not None else os.environ.get(ENV_API_KEY, "")
        if not key.strip():
            raise ValueError(f"no agent API key: pass api_key or set {ENV_API_KEY}")
        parsed = parse_secret(key)
        if parsed.key_id is None:
            raise ValueError(
                "the key was issued before 0.19.0 and names no key id: use Client(engine_url, AgentKey.parse(secret), key_id)"
            )
        if engine_url is None:
            engine_url = os.environ.get(ENV_ENGINE)
        return cls(
            engine_url_for(parsed, engine_url),
            parsed.key,
            parsed.key_id,
            timeout=timeout,
            retries=retries,
        )

    # Proposals

    def propose(self, proposal: Proposal) -> dict[str, Any]:
        """Sign the proposal with the agent's key (unless already signed) and
        submit it. Returns the decision as the engine recorded it."""
        if not proposal.key_id:
            proposal.key_id = self.key_id
        if proposal.signature is None:
            proposal.sign(self.agent_key)
        return self._call("POST", "/proposals", body=proposal.to_wire())

    # Reads, scoped to this agent

    def decisions(
        self,
        *,
        connected_account_id: str | None = None,
        verdict: str | None = None,
        since: str | None = None,
        until: str | None = None,
        cursor: str | None = None,
        limit: int | None = None,
    ) -> dict[str, Any]:
        """A page of this agent's decisions, newest first."""
        params = {
            "connectedAccountId": connected_account_id,
            "verdict": verdict,
            "from": since,
            "to": until,
            "cursor": cursor,
            "limit": limit,
        }
        return self._agent_call("GET", "/agent/decisions", query=params)

    def decision(self, decision_id: str) -> dict[str, Any]:
        return self._agent_call(
            "GET", f"/agent/decisions/{urllib.parse.quote(decision_id, safe='')}"
        )

    def approvals(self, status: str | None = None) -> list[dict[str, Any]]:
        return self._agent_call("GET", "/agent/approvals", query={"status": status})

    def positions(self) -> list[dict[str, Any]]:
        return self._agent_call("GET", "/agent/positions")

    def pnl(self, start: str | None = None, end: str | None = None) -> dict[str, Any]:
        """Daily PnL of this agent; ``start`` and ``end`` are dates (YYYY-MM-DD)."""
        return self._agent_call("GET", "/agent/pnl", query={"from": start, "to": end})

    def connected_account(self, connected_account_id: str) -> ConnectedAccount:
        """A connected account as this agent may see it, its setup without any secret."""
        return self._agent_call("GET", _account_path(connected_account_id))

    def venue_rules(
        self, connected_account_id: str, symbol: str | None = None
    ) -> dict[str, Any]:
        """What the connected account's venue accepts per symbol (tick, step,
        minimums), as the engine last read it; ``symbol`` narrows it to one."""
        return self._agent_call(
            "GET",
            _account_path(connected_account_id, "/venue-rules"),
            query={"symbol": symbol},
        )

    # Connected-account data. The agent reads each connected account it was
    # given whole, other agents' and the owner's activity included, and every
    # read is written to the account's access log. On hosted Mishmar these
    # (with connected_account) are answered while the account's engine
    # sleeps, so an agent that only reads never wakes it. Times are RFC 3339
    # strings or timezone-aware datetimes.

    def me(self) -> AgentIdentity:
        """Who the key belongs to: the agent, its account and portfolio, and
        the key id. The engine answers only a key it knows, so this is the
        read that proves one."""
        return self._agent_call("GET", "/agent/me")

    def connected_accounts(self) -> list[ConnectedAccount]:
        """The connected accounts this agent was given, in the order it got them."""
        return self._agent_call("GET", "/agent/connected-accounts")

    def account_balances(
        self, connected_account_id: str, *, at: str | datetime | None = None
    ) -> BalanceSnapshot:
        """The connected account's balances at the venue, spot and margin, each
        with its value in USD, at ``at`` (default now). A moment before the
        account's history or the plan's retention is a 404."""
        return self._agent_call(
            "GET",
            _account_path(connected_account_id, "/balances"),
            query={"at": _when(at)},
        )

    def account_positions(
        self, connected_account_id: str, *, at: str | datetime | None = None
    ) -> AccountPositionSnapshot:
        """The venue's own net positions on the connected account, whoever
        placed the orders behind them, at ``at`` (default now). This agent's
        own book is :meth:`positions`."""
        return self._agent_call(
            "GET",
            _account_path(connected_account_id, "/positions"),
            query={"at": _when(at)},
        )

    def account_transactions(
        self,
        connected_account_id: str,
        *,
        since: str | datetime | None = None,
        until: str | datetime | None = None,
        kind: AccountTransactionKind | None = None,
        cursor: str | None = None,
        limit: int | None = None,
    ) -> AccountTransactionPage:
        """A page of the connected account's trades, transfers, funding and
        fees as the venue reports them, newest first. A trade's ``origin``
        says who placed it; ``origin["own"]`` is true on this agent's own.
        Pass the page's ``nextCursor`` as ``cursor`` for the page after;
        ``limit`` is 1 to 500 (the engine's default 100)."""
        return self._agent_call(
            "GET",
            _account_path(connected_account_id, "/transactions"),
            query={
                "from": _when(since),
                "to": _when(until),
                "kind": kind,
                "cursor": cursor,
                "limit": limit,
            },
        )

    def account_equity(
        self,
        connected_account_id: str,
        *,
        since: str | datetime | None = None,
        until: str | datetime | None = None,
        resolution: EquityResolution | None = None,
    ) -> EquitySeries:
        """The connected account's value in USD over time, oldest point first.
        Without ``resolution`` (minute, hour, day) the engine picks one that
        gives a few hundred points over the range."""
        return self._agent_call(
            "GET",
            _account_path(connected_account_id, "/equity"),
            query={"from": _when(since), "to": _when(until), "resolution": resolution},
        )

    def wait(
        self, decision_id: str, timeout: float = 60.0, interval: float = 1.0
    ) -> dict[str, Any]:
        """Poll a decision until it is no longer waiting for a person, or
        until timeout; returns the last decision read either way."""
        deadline = time.monotonic() + timeout
        while True:
            d = self.decision(decision_id)
            if resolved(d) or time.monotonic() >= deadline:
                return d
            time.sleep(min(interval, max(0.0, deadline - time.monotonic())))

    # The signer channel

    def signer_poll(self, wait_seconds: int = 25) -> list[dict[str, Any]]:
        """Wait up to wait_seconds for requests this agent's signer must sign."""
        return self._agent_call(
            "POST",
            "/agent/signer/poll",
            body={"waitSeconds": wait_seconds},
            timeout=self.timeout + wait_seconds,
        )

    def signer_commitments(
        self, venue_key_id: str, count: int | None = None
    ) -> dict[str, Any]:
        """Take up to ``count`` of the engine's single-use FROST commitments for
        one-shot signing with a venue key (CommitmentBatch); by default as many
        as the account allows."""
        body: dict[str, Any] = {"venueKeyId": venue_key_id}
        if count is not None:
            body["count"] = count
        return self._agent_call("POST", "/agent/signer/commitments", body=body)

    def signer_answer(self, request_id: str, answer: dict[str, Any]) -> None:
        """Answer a sign request: a share, a signature, a venue response or a refusal (SignerAnswer)."""
        self._agent_call(
            "POST",
            f"/agent/signer/requests/{urllib.parse.quote(request_id, safe='')}/answer",
            body=answer,
        )

    # Plumbing

    def _agent_call(
        self,
        method: str,
        path: str,
        *,
        query: dict[str, Any] | None = None,
        body: Any = None,
        timeout: float | None = None,
    ) -> Any:
        params = {k: v for k, v in (query or {}).items() if v is not None}
        query_text = urllib.parse.urlencode(params)
        data = b"" if body is None else json.dumps(body).encode("utf-8")

        # Each attempt is signed anew, with its own nonce and time, so a
        # retry is never a replay.
        def sign() -> dict[str, str]:
            return self.agent_key.sign_request(
                self.key_id, method, path, query=query_text, body=data
            )

        return self._call(
            method,
            path + ("?" + query_text if query_text else ""),
            body=body,
            sign=sign,
            timeout=timeout,
        )

    def _call(
        self,
        method: str,
        path: str,
        *,
        body: Any = None,
        sign: Callable[[], dict[str, str]] | None = None,
        timeout: float | None = None,
    ) -> Any:
        """Send the request, again after an answer that did nothing (``retries``),
        and return the final answer decoded; ``sign`` makes each attempt's headers."""
        data = None if body is None else json.dumps(body).encode("utf-8")
        start = time.monotonic()
        attempt = 0
        while True:
            try:
                return self._once(method, path, data, sign() if sign else {}, timeout)
            except EngineError as err:
                wait = (
                    self.retries.wait(
                        attempt, err.status, err.retry_after, time.monotonic() - start
                    )
                    if self.retries is not None
                    else None
                )
                if wait is None:
                    raise
                self._sleep(wait)
                attempt += 1

    def _once(
        self,
        method: str,
        path: str,
        data: bytes | None,
        headers: dict[str, str],
        timeout: float | None,
    ) -> Any:
        req = urllib.request.Request(self.engine_url + path, data=data, method=method)
        req.add_header("Accept", "application/json")
        if data is not None:
            req.add_header("Content-Type", "application/json")
        for k, v in headers.items():
            req.add_header(k, v)
        try:
            with urllib.request.urlopen(req, timeout=timeout or self.timeout) as res:
                raw = res.read()
        except urllib.error.HTTPError as err:
            raw = err.read()
            code, message = "http_error", err.reason or ""
            try:
                parsed = json.loads(raw)
                code = parsed.get("code", code)
                message = parsed.get("message", message)
            except (ValueError, AttributeError):
                if raw:
                    message = raw.decode("utf-8", "replace")[:200]
            failure = EngineError(err.code, str(code), str(message))
            failure.retry_after = (
                err.headers.get("Retry-After") if err.headers else None
            )
            raise failure from None
        return json.loads(raw) if raw else None


def _account_path(connected_account_id: str, sub: str = "") -> str:
    """The agent-facing path of a connected account, or of one of its reads."""
    return f"/agent/connected-accounts/{urllib.parse.quote(connected_account_id, safe='')}{sub}"


def resolved(decision: dict[str, Any]) -> bool:
    """Whether the decision no longer waits for a person: anything but
    require_approval, or an approval outcome that has been decided."""
    if decision.get("verdict") != "require_approval":
        return True
    approval = decision.get("approval") or {}
    return approval.get("status") in RESOLVED_APPROVAL
