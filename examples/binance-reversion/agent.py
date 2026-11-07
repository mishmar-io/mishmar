"""Binance Reversion Agent: a test bot for a Binance demo-trading account.

It trades through the agent's signer only and holds no key. By default the
Python SDK runs the signer as a child of this process; with --connect it
attaches to one that already runs (`mishmar signer run --local unix:…`), so
the same signer can also answer the console's key verification. It walks a
fixed sequence of orders chosen against these rules, set up in the console
on the agent and on its portfolio:

  agent      Reversion max order        order > $600         block
  agent      Reversion order review     order > $250         require_approval
  agent      Reversion agent gross      gross > $750         block
  portfolio  Reversion ETH review       ETH net > $100       require_approval
  portfolio  Reversion portfolio gross  gross > $600         block
  portfolio  Reversion daily loss       daily loss > $100    block
  portfolio  Reversion leverage         leverage > 1         block   (budget $2000)

Each step states the verdict it expects and the rules that must fail, and the
run ends with a pass/fail table, the agent's positions and its PnL. A step
that parks for a person waits for someone to approve or reject it in the
console and reports what they chose. Binance's minimum order is $50 on
BTCUSDT and $20 on ETHUSDT, which is why the sizes are larger than the
Hyperliquid test's.

    pip install mishmar      # plus the mishmar binary on the PATH or in MISHMAR_BIN
    python agent.py --trading-account ta_… --results results.json
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
import urllib.request
from dataclasses import dataclass, field
from decimal import ROUND_CEILING, ROUND_DOWN, Decimal
from typing import Any, Callable

from mishmar import EngineError, Signer

BN_FAPI = "https://demo-fapi.binance.com/fapi/v1"


def mid(symbol: str) -> Decimal:
    with urllib.request.urlopen(
        f"{BN_FAPI}/ticker/bookTicker?symbol={symbol}", timeout=10
    ) as r:
        t = json.load(r)
    return (Decimal(t["bidPrice"]) + Decimal(t["askPrice"])) / 2


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
        self.rules: dict[str, dict[str, Any]] = {}
        self.decisions: dict[str, dict[str, Any]] = {}

    # Sizing

    def step_size(self, symbol: str) -> Decimal:
        if symbol not in self.rules:
            self.rules[symbol] = self.s.venue_rules(self.ta, symbol)["symbols"][0]
        return Decimal(self.rules[symbol]["stepSize"])

    def size_for(
        self, symbol: str, usd: str, price: Decimal | None = None, up: bool = False
    ) -> str:
        price = price or mid(symbol)
        step = self.step_size(symbol)
        size = (Decimal(usd) / price / step).to_integral_value(
            ROUND_CEILING if up else ROUND_DOWN
        ) * step
        return str(size.normalize())

    def market(
        self, symbol: str, side: str, usd: str, valid_for: int = 60
    ) -> dict[str, Any]:
        return {
            "tradingAccountId": self.ta,
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
            if p["symbol"] == symbol and p.get("tradingAccountId", self.ta) == self.ta:
                return p
        return None

    # Running a step

    def run(self, step: Step) -> dict[str, Any]:
        expect, failing = (step.expect, step.failing)
        print(f"\n== {step.name}")
        if step.expect_fn:
            expect, failing = step.expect_fn(self)
        intent = step.intent(self)
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
        if sorted(failing) != failed and (failing or expect != "allow"):
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
            f"(approve or reject it in the console; waiting up to {int(self.approval_timeout)} s)",
            flush=True,
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
        print(f"   venue: {json.dumps(d['venueAck'])}")
    if d.get("venueError"):
        print(f"   venue error: {json.dumps(d['venueError'])}")


# Checks run after a step


def resting(agent: Agent, d: dict[str, Any]) -> list[str]:
    ack = d.get("venueAck")
    if not ack:
        return ["no venue ack"]
    return (
        []
        if ack["status"] in ("resting", "open", "accepted", "new")
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


def filled_if_allowed(agent: Agent, d: dict[str, Any]) -> list[str]:
    return filled(agent, d) if d["verdict"] == "allow" else []


def not_forwarded(agent: Agent, d: dict[str, Any]) -> list[str]:
    if (d.get("approval") or {}).get("status") == "approved":
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


REST = "1. passive limit buy BTCUSDT 3% below the market rests"


def steps(sequence: str = "full") -> list[Step]:
    def rest_buy(a: Agent) -> dict[str, Any]:
        price = (mid("BTCUSDT") * Decimal("0.97")).quantize(
            Decimal(1), rounding=ROUND_DOWN
        )
        return {
            "tradingAccountId": a.ta,
            "symbol": "BTCUSDT",
            "side": "buy",
            "size": a.size_for("BTCUSDT", "60", price=price, up=True),
            "orderType": "limit",
            "price": str(price),
            "reduceOnly": False,
            "validForSeconds": 60,
        }

    def cancel_rest(a: Agent) -> dict[str, Any]:
        coid = a.decisions[REST]["proposal"]["clientOrderId"]
        return {
            "tradingAccountId": a.ta,
            "symbol": "BTCUSDT",
            "kind": "cancel",
            "targetClientOrderId": coid,
            "validForSeconds": 60,
        }

    def gross_after(a: Agent, usd: int) -> Decimal:
        g = a.gross()
        print(f"   (agent gross now ${g:.2f}; about ${g + usd:.2f} after this order)")
        return g + usd

    def big_order(a: Agent) -> tuple[str, list[str]]:
        after = gross_after(a, 700)
        failing = ["Reversion max order", "Reversion order review"]
        if after > 750:
            failing.append("Reversion agent gross")
        if after > 600:
            failing.append("Reversion portfolio gross")
        return "block", failing

    def portfolio_cap(a: Agent) -> tuple[str, list[str]]:
        return (
            ("block", ["Reversion portfolio gross"])
            if gross_after(a, 240) > 600
            else ("allow", [])
        )

    def exit_btc(a: Agent) -> dict[str, Any]:
        p = a.position("BTCUSDT")
        size = p["size"].lstrip("-") if p else "0"
        return {
            "tradingAccountId": a.ta,
            "symbol": "BTCUSDT",
            "side": "sell",
            "size": size,
            "orderType": "market",
            "reduceOnly": True,
            "validForSeconds": 60,
        }

    exit_step = Step(
        "8. reduce-only exit of the whole BTCUSDT position",
        "allow",
        exit_btc,
        after=flat("BTCUSDT"),
    )
    if sequence == "cap":
        # The portfolio's gross cap alone: two orders under the review threshold
        # build about $400, so a third under it crosses $600 and nothing else.
        return [
            Step(
                "c1. $200 market buy BTCUSDT",
                "allow",
                lambda a: a.market("BTCUSDT", "buy", "200"),
                after=filled,
            ),
            Step(
                "c2. $200 market buy BTCUSDT",
                "allow",
                lambda a: a.market("BTCUSDT", "buy", "200"),
                after=filled,
            ),
            Step(
                "c3. $240 market buy BTCUSDT (portfolio gross cap)",
                "block",
                lambda a: a.market("BTCUSDT", "buy", "240"),
                failing=["Reversion portfolio gross"],
            ),
            exit_step,
        ]
    return [
        Step(REST, "allow", rest_buy, after=resting),
        Step("2. cancel the resting order", "allow", cancel_rest),
        Step(
            "3. $110 market buy BTCUSDT",
            "allow",
            lambda a: a.market("BTCUSDT", "buy", "110"),
            after=filled,
        ),
        Step(
            "4. $150 market buy ETHUSDT (portfolio ETH review)",
            "require_approval",
            lambda a: a.market("ETHUSDT", "buy", "150", valid_for=1200),
            failing=["Reversion ETH review"],
            after=not_forwarded,
        ),
        Step(
            "5. $700 market buy BTCUSDT (agent max order)",
            "block",
            lambda a: a.market("BTCUSDT", "buy", "700"),
            expect_fn=big_order,
        ),
        Step(
            "6. $300 market buy BTCUSDT (agent large-order review)",
            "require_approval",
            lambda a: a.market("BTCUSDT", "buy", "300", valid_for=1200),
            failing=["Reversion order review"],
            after=filled,
        ),
        Step(
            "7. $240 market buy BTCUSDT (portfolio gross cap)",
            "block",
            lambda a: a.market("BTCUSDT", "buy", "240"),
            expect_fn=portfolio_cap,
            after=filled_if_allowed,
        ),
        exit_step,
    ]


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument(
        "--config",
        default=os.environ.get("MISHMAR_AGENT_CONFIG"),
        help="the signer's config, written by `mishmar signer init` (default ~/.mishmar/agent.json)",
    )
    ap.add_argument(
        "--connect",
        metavar="ADDRESS",
        help="attach to a signer that already runs on this address (unix:/path, or host:port "
        "with its token in MISHMAR_LOCAL_TOKEN) instead of starting one",
    )
    ap.add_argument(
        "--trading-account",
        default=os.environ.get("MISHMAR_TRADING_ACCOUNT"),
        required="MISHMAR_TRADING_ACCOUNT" not in os.environ,
        help="the trading account's id, ta_…",
    )
    ap.add_argument("--approval-timeout", type=float, default=900)
    ap.add_argument("--results", default="results.json")
    ap.add_argument("--only", help="comma-separated step numbers to run")
    ap.add_argument(
        "--sequence",
        choices=["full", "cap"],
        default="full",
        help="cap: only the portfolio's gross cap, with no approval to wait for",
    )
    args = ap.parse_args()

    only = {int(x) for x in args.only.split(",")} if args.only else None
    results: list[dict[str, Any]] = []
    signer = (
        Signer.connect(args.connect)
        if args.connect
        else Signer(args.config, log_level="info")
    )
    with signer as s:
        agent = Agent(s, args.trading_account, args.approval_timeout)
        print(f"signer up: {s.health()}", flush=True)
        for i, step in enumerate(steps(args.sequence), start=1):
            if only and i not in only:
                continue
            results.append(agent.run(step))
            sys.stdout.flush()
            time.sleep(1.5)

        time.sleep(4)
        positions = s.positions()
        pnl = s.pnl()
        page = s.decisions(trading_account_id=args.trading_account, limit=50)

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
        f"\n== decisions recorded for the agent: {len(page['items'])} "
        f"(newest {page['items'][0]['id'] if page['items'] else '-'})"
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
