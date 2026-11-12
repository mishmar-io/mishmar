"""HL Trend Follower: a test bot for a Hyperliquid testnet connected account.

It trades through the agent's signer only (the Python SDK's Signer runs
`mishmar signer run --local` as a child; this process holds no key) and walks
a fixed sequence of orders chosen against these rules, set up in the console
on the agent and on its portfolio:

  agent      Max order size            order > $150         block
  agent      Review large orders       order > $40          require_approval
  agent      Agent gross exposure      gross > $150         block
  portfolio  ETH exposure review       ETH net > $30        require_approval
  portfolio  Portfolio gross exposure  gross > $100         block
  portfolio  Daily loss stop           daily loss > $25     block
  portfolio  Leverage cap              leverage > 1         block

Each step states the verdict it expects and the rules that must fail, and the
run ends with a pass/fail table, the agent's positions and its PnL. A step
that parks for a person waits for someone to approve or reject it in the
console and reports what they chose.

    pip install mishmar      # plus the mishmar binary on the PATH or in MISHMAR_BIN
    python agent.py --connected-account ta_… --results results.json
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
import urllib.request
from dataclasses import dataclass, field
from decimal import ROUND_DOWN, Decimal
from typing import Any, Callable

from mishmar import EngineError, Signer

HL_INFO = "https://api.hyperliquid-testnet.xyz/info"


def mids() -> dict[str, Decimal]:
    req = urllib.request.Request(
        HL_INFO,
        data=json.dumps({"type": "allMids"}).encode(),
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=10) as r:
        return {k: Decimal(v) for k, v in json.load(r).items()}


@dataclass
class Step:
    name: str
    expect: str  # allow | require_approval | block
    intent: Callable[["Agent"], dict[str, Any]]
    failing: list[str] = field(default_factory=list)  # rule names that must fail
    expect_fn: Callable[["Agent"], tuple[str, list[str]]] | None = None
    after: Callable[["Agent", dict[str, Any]], list[str]] | None = None


class Agent:
    def __init__(self, signer: Signer, ta: str, approval_timeout: float):
        self.s = signer
        self.ta = ta
        self.approval_timeout = approval_timeout
        self.sz: dict[str, int] = {}
        self.decisions: dict[str, dict[str, Any]] = {}

    # Sizing

    def size_decimals(self, symbol: str) -> int:
        if symbol not in self.sz:
            rules = self.s.venue_rules(self.ta, symbol)
            self.sz[symbol] = int(rules["symbols"][0]["sizeDecimals"])
        return self.sz[symbol]

    def size_for(self, symbol: str, usd: str) -> str:
        mid = mids()[symbol]
        q = Decimal(1).scaleb(-self.size_decimals(symbol))
        return str((Decimal(usd) / mid).quantize(q, rounding=ROUND_DOWN))

    def market(
        self, symbol: str, side: str, usd: str, valid_for: int = 60
    ) -> dict[str, Any]:
        return {
            "connectedAccountId": self.ta,
            "symbol": symbol,
            "side": side,
            "size": self.size_for(symbol, usd),
            "orderType": "market",
            "reduceOnly": False,
            "validForSeconds": valid_for,
        }

    def gross(self) -> Decimal:
        return sum(
            (abs(Decimal(p["notionalUsd"])) for p in self.s.positions()), Decimal(0)
        )

    def position(self, symbol: str) -> dict[str, Any] | None:
        for p in self.s.positions():
            if p["symbol"] == symbol and p.get("connectedAccountId", self.ta) == self.ta:
                return p
        return None

    # Running a step

    def run(self, step: Step) -> dict[str, Any]:
        expect, failing = (step.expect, step.failing)
        if step.expect_fn:
            expect, failing = step.expect_fn(self)
        intent = step.intent(self)
        print(f"\n== {step.name}")
        print(
            f"   intent: {intent.get('kind', 'order')} {intent.get('side', '')} "
            f"{intent.get('size', '')} {intent['symbol']} {intent.get('orderType', '')} "
            f"{intent.get('price', '')}".rstrip()
        )
        t0 = time.monotonic()
        try:
            d = self.s.propose(intent)
        except EngineError as e:
            print(f"   ERROR {e}")
            return {"step": step.name, "ok": False, "problems": [f"engine error: {e}"]}
        took = (time.monotonic() - t0) * 1000
        self.decisions[step.name] = d
        show(d, took)
        problems: list[str] = []
        if d["verdict"] != expect:
            problems.append(f"verdict {d['verdict']}, expected {expect}")
        failed = sorted(
            e["ruleName"]
            for e in d.get("evaluations", [])
            if not e["passed"] and not e.get("waived")
        )
        if failing and sorted(failing) != failed:
            problems.append(f"failing rules {failed}, expected {sorted(failing)}")
        if d["verdict"] == "require_approval":
            d = self.await_person(d)
            self.decisions[step.name] = d
        if step.after:
            problems += step.after(self, d)
        ok = not problems
        print(
            f"   {'PASS' if ok else 'FAIL'}"
            + ("" if ok else ": " + "; ".join(problems))
        )
        return {
            "step": step.name,
            "ok": ok,
            "problems": problems,
            "decisionId": d["id"],
            "verdict": d["verdict"],
            "approval": (d.get("approval") or {}).get("status"),
            "failedRules": failed,
        }

    def await_person(self, d: dict[str, Any]) -> dict[str, Any]:
        print(
            f"   parked for a person: approval {d.get('approvalId')} "
            f"(approve or reject it in the console; waiting up to {int(self.approval_timeout)} s)"
        )
        d = self.s.wait(d["id"], timeout=self.approval_timeout, interval=2.0)
        a = d.get("approval") or {}
        print(
            f"   person: {a.get('status')} by {a.get('decidedBy')} at {a.get('decidedAt')}"
        )
        if a.get("status") == "approved":
            # Forwarding after the approval goes through the signer channel; give it a moment.
            for _ in range(20):
                if d.get("venueAck") or (d.get("signing") or {}).get("status") in (
                    "refused",
                    "unreachable",
                ):
                    break
                time.sleep(1)
                d = self.s.decision(d["id"])
            show(d, None)
        return d


def show(d: dict[str, Any], took: float | None) -> None:
    p = d["proposal"]
    line = f"   {d['id']}: {d['verdict'].upper()}"
    if d.get("reason"):
        line += f" ({d['reason']})"
    print(line)
    print(
        f"   notional ${p['notionalUsd']} at {p.get('price', '-')}; cloid {p['clientOrderId']}"
        + ("; exit" if d.get("exit") else "")
        + (f"; engine {d['latencyMs']:.1f} ms" if "latencyMs" in d else "")
        + (f"; round trip {took:.0f} ms" if took is not None else "")
    )
    for e in d.get("evaluations", []):
        mark = "waived" if e.get("waived") else ("ok" if e["passed"] else "FAILED")
        print(
            f"     [{e['scope']['kind']:9}] {e['ruleName']:26} {e['metric']:15} "
            f"{e['observed']:>10} vs {e['threshold']:>6} {e['unit']:5} -> {mark}"
            + (f" ({e['outcome']})" if not e["passed"] and not e.get("waived") else "")
        )
    if d.get("signing"):
        sg = d["signing"]
        print(
            f"   signer: {sg['status']}"
            + (f" ({sg['detail']})" if sg.get("detail") else "")
        )
    if d.get("venueAck"):
        a = d["venueAck"]
        print(f"   venue: {a['status']} order {a['orderId']}")


# Checks run after a step


def resting(agent: Agent, d: dict[str, Any]) -> list[str]:
    ack = d.get("venueAck")
    if not ack:
        return ["no venue ack"]
    return (
        []
        if ack["status"] in ("resting", "open", "accepted")
        else [f"venue status {ack['status']}, expected resting"]
    )


def filled(agent: Agent, d: dict[str, Any]) -> list[str]:
    if (
        d["verdict"] == "require_approval"
        and (d.get("approval") or {}).get("status") != "approved"
    ):
        return [] if not d.get("venueAck") else ["forwarded although not approved"]
    ack = d.get("venueAck")
    if not ack:
        return ["no venue ack"]
    return (
        []
        if ack["status"] == "filled"
        else [f"venue status {ack['status']}, expected filled"]
    )


def not_forwarded(agent: Agent, d: dict[str, Any]) -> list[str]:
    status = (d.get("approval") or {}).get("status")
    if status == "approved":
        return filled(agent, d)
    return ["forwarded although not approved"] if d.get("venueAck") else []


def flat(symbol: str) -> Callable[[Agent, dict[str, Any]], list[str]]:
    def check(agent: Agent, d: dict[str, Any]) -> list[str]:
        problems = filled(agent, d)
        if not d.get("exit"):
            problems.append("not judged an exit")
        time.sleep(3)
        p = agent.position(symbol)
        if p and Decimal(p["size"]) != 0:
            problems.append(f"still holding {p['size']} {symbol}")
        return problems

    return check


def steps() -> list[Step]:
    def rest_buy(a: Agent) -> dict[str, Any]:
        price = int(mids()["BTC"] * Decimal("0.97"))
        return {
            "connectedAccountId": a.ta,
            "symbol": "BTC",
            "side": "buy",
            "size": str(
                (Decimal(15) / price).quantize(
                    Decimal(1).scaleb(-a.size_decimals("BTC")), rounding=ROUND_DOWN
                )
                + Decimal("0.00001")
            ),
            "orderType": "limit",
            "price": str(price),
            "reduceOnly": False,
            "validForSeconds": 60,
        }

    def cancel_rest(a: Agent) -> dict[str, Any]:
        coid = a.decisions["1. small passive limit buy rests"]["proposal"][
            "clientOrderId"
        ]
        return {
            "connectedAccountId": a.ta,
            "symbol": "BTC",
            "kind": "cancel",
            "targetClientOrderId": coid,
            "validForSeconds": 60,
        }

    def gross_step(a: Agent) -> tuple[str, list[str]]:
        after = a.gross() + 30
        print(f"   (agent gross now ${a.gross():.2f}; ${after:.2f} after this order)")
        return ("block", ["Portfolio gross exposure"]) if after > 100 else ("allow", [])

    def exit_btc(a: Agent) -> dict[str, Any]:
        p = a.position("BTC")
        size = p["size"].lstrip("-") if p else "0"
        return {
            "connectedAccountId": a.ta,
            "symbol": "BTC",
            "side": "sell",
            "size": size,
            "orderType": "market",
            "reduceOnly": True,
            "validForSeconds": 60,
        }

    return [
        Step("1. small passive limit buy rests", "allow", rest_buy, after=resting),
        Step("2. cancel the resting order", "allow", cancel_rest),
        Step(
            "3. $20 market buy BTC",
            "allow",
            lambda a: a.market("BTC", "buy", "20"),
            after=filled,
        ),
        Step(
            "4. $35 market buy ETH (portfolio ETH review)",
            "require_approval",
            lambda a: a.market("ETH", "buy", "35", valid_for=1200),
            failing=["ETH exposure review"],
            after=not_forwarded,
        ),
        Step(
            "5. $200 market buy BTC (agent max order)",
            "block",
            lambda a: a.market("BTC", "buy", "200"),
            failing=[
                "Max order size",
                "Review large orders",
                "Agent gross exposure",
                "Portfolio gross exposure",
            ],
        ),
        Step(
            "6. $60 market buy BTC (agent large-order review)",
            "require_approval",
            lambda a: a.market("BTC", "buy", "60", valid_for=1200),
            failing=["Review large orders"],
            after=filled,
        ),
        Step(
            "7. $30 market buy BTC (portfolio gross cap)",
            "block",
            lambda a: a.market("BTC", "buy", "30"),
            expect_fn=gross_step,
            after=filled_if_allowed,
        ),
        Step(
            "8. reduce-only exit of the whole BTC position",
            "allow",
            exit_btc,
            after=flat("BTC"),
        ),
    ]


def filled_if_allowed(agent: Agent, d: dict[str, Any]) -> list[str]:
    return filled(agent, d) if d["verdict"] == "allow" else []


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument(
        "--config",
        default=os.environ.get("MISHMAR_AGENT_CONFIG"),
        help="the signer's config, written by the signer's first start with MISHMAR_API_KEY, or `mishmar signer init` (default ~/.mishmar/agent.json)",
    )
    ap.add_argument(
        "--connected-account",
        default=os.environ.get("MISHMAR_CONNECTED_ACCOUNT"),
        required="MISHMAR_CONNECTED_ACCOUNT" not in os.environ,
        help="the connected account's id, ta_…",
    )
    ap.add_argument("--approval-timeout", type=float, default=900)
    ap.add_argument("--results", default="results.json")
    ap.add_argument("--only", help="comma-separated step numbers to run")
    args = ap.parse_args()

    only = {int(x) for x in args.only.split(",")} if args.only else None
    results: list[dict[str, Any]] = []
    with Signer(args.config, log_level="info") as s:
        agent = Agent(s, args.connected_account, args.approval_timeout)
        print(f"signer up: {s.health()}")
        for i, step in enumerate(steps(), start=1):
            if only and i not in only:
                continue
            results.append(agent.run(step))
            time.sleep(1.5)

        time.sleep(4)
        positions = s.positions()
        pnl = s.pnl()
        page = s.decisions(connected_account_id=args.connected_account, limit=50)

    print("\n== result")
    for r in results:
        print(
            f"   {'PASS' if r['ok'] else 'FAIL'}  {r['step']}"
            + (f"  ({r['approval']})" if r.get("approval") else "")
            + ("" if r["ok"] else "  -> " + "; ".join(r["problems"]))
        )
    print("\n== positions (agent)")
    for p in positions:
        print(
            f"   {p['symbol']} {p['side']} {p['size']} @ {p['entryPrice']} mark {p['markPrice']} "
            f"unrealized ${p['unrealizedUsd']}"
        )
    if not positions:
        print("   flat")
    t = pnl["totals"]
    print("\n== pnl (agent, today)")
    print(
        f"   realized ${t['realizedUsd']}  unrealized ${t['unrealizedUsd']}  fees ${t['feesUsd']}  "
        f"funding ${t['fundingUsd']}  net ${t['netUsd']}  realized net ${t['realizedNetUsd']}"
    )
    print(
        f"\n== decisions recorded for the agent: {len(page['items'])} (newest {page['items'][0]['id'] if page['items'] else '-'})"
    )

    with open(args.results, "w") as f:
        json.dump(
            {
                "results": results,
                "decisions": agent.decisions,
                "positions": positions,
                "pnl": pnl,
                "agentDecisions": page,
            },
            f,
            indent=2,
        )
    return 0 if all(r["ok"] for r in results) else 1


if __name__ == "__main__":
    sys.exit(main())
