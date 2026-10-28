"""A small HTTP client for the engine, on the standard library only.

Proposals go to POST /proposals with the signature in the body. Everything
else an agent may call (its own decisions, approvals, positions, PnL, trading accounts,
and the signer channel) is under /agent and signed with the agentAuth
headers, so a bot can function without an operator token and cannot act on
the account.
"""

from __future__ import annotations

import json
import time
import urllib.error
import urllib.parse
import urllib.request
from typing import Any

from .agentkey import AgentKey
from .proposal import Proposal

RESOLVED_APPROVAL = ("approved", "rejected", "expired")


class EngineError(Exception):
    """The engine answered with an error: ``status`` (HTTP), ``code`` and ``message`` from its body."""

    def __init__(self, status: int, code: str, message: str):
        super().__init__(f"{status} {code}: {message}")
        self.status, self.code, self.message = status, code, message


class Client:
    """Talks to one engine as one agent. The trading key's share lives in the
    agent's signer, never here: this client proposes, reads, and relays the
    signer channel."""

    def __init__(
        self,
        engine_url: str,
        agent_key: AgentKey,
        key_id: str,
        *,
        timeout: float = 10.0,
    ):
        self.engine_url = engine_url.rstrip("/")
        self.agent_key = agent_key
        self.key_id = key_id
        self.timeout = timeout

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
        trading_account_id: str | None = None,
        verdict: str | None = None,
        since: str | None = None,
        until: str | None = None,
        cursor: str | None = None,
        limit: int | None = None,
    ) -> dict[str, Any]:
        """A page of this agent's decisions, newest first."""
        params = {
            "tradingAccountId": trading_account_id,
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

    def trading_account(self, trading_account_id: str) -> dict[str, Any]:
        """A trading account as this agent may see it, its setup without any secret."""
        return self._agent_call(
            "GET",
            f"/agent/trading-accounts/{urllib.parse.quote(trading_account_id, safe='')}",
        )

    def venue_rules(
        self, trading_account_id: str, symbol: str | None = None
    ) -> dict[str, Any]:
        """What the trading account's venue accepts per symbol (tick, step,
        minimums), as the engine last read it; ``symbol`` narrows it to one."""
        return self._agent_call(
            "GET",
            f"/agent/trading-accounts/{urllib.parse.quote(trading_account_id, safe='')}/venue-rules",
            query={"symbol": symbol},
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
        headers = self.agent_key.sign_request(
            self.key_id, method, path, query=query_text, body=data
        )
        return self._call(
            method,
            path + ("?" + query_text if query_text else ""),
            body=body,
            headers=headers,
            timeout=timeout,
        )

    def _call(
        self,
        method: str,
        path: str,
        *,
        body: Any = None,
        headers: dict[str, str] | None = None,
        timeout: float | None = None,
    ) -> Any:
        data = None if body is None else json.dumps(body).encode("utf-8")
        req = urllib.request.Request(self.engine_url + path, data=data, method=method)
        req.add_header("Accept", "application/json")
        if data is not None:
            req.add_header("Content-Type", "application/json")
        for k, v in (headers or {}).items():
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
            raise EngineError(err.code, str(code), str(message)) from None
        return json.loads(raw) if raw else None


def resolved(decision: dict[str, Any]) -> bool:
    """Whether the decision no longer waits for a person: anything but
    require_approval, or an approval outcome that has been decided."""
    if decision.get("verdict") != "require_approval":
        return True
    approval = decision.get("approval") or {}
    return approval.get("status") in RESOLVED_APPROVAL
