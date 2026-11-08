"""The smallest bot: start the agent's signer, read the trading account and the
venue's rules for a symbol, place one small limit order, wait if a person has
to approve it, and print the verdict. The signer holds the agent's key and its
half of the trading key; this process holds nothing.

    pip install mishmar      # plus the mishmar binary on the PATH or in MISHMAR_BIN
    python agent.py --trading-account ta_… --symbol BTC --size 0.001 --price 50000
"""

from __future__ import annotations

import argparse
import os

from mishmar import Signer


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument(
        "--config",
        default=os.environ.get("MISHMAR_AGENT_CONFIG"),
        help="the signer's config, written by the signer's first start with MISHMAR_API_KEY, or `mishmar signer init` (default ~/.mishmar/agent.json)",
    )
    ap.add_argument(
        "--trading-account",
        default=os.environ.get("MISHMAR_TRADING_ACCOUNT"),
        required="MISHMAR_TRADING_ACCOUNT" not in os.environ,
        help="the trading account's id, ta_…",
    )
    ap.add_argument(
        "--symbol", default="BTC", help="BTC on Hyperliquid, BTCUSDT on Binance"
    )
    ap.add_argument("--side", default="buy", choices=["buy", "sell"])
    ap.add_argument(
        "--size", default="0.001", help="in the venue's units, on its step size"
    )
    ap.add_argument(
        "--price",
        required=True,
        help="the limit price; far from the market, the order rests",
    )
    args = ap.parse_args()

    with Signer(args.config) as s:
        account = s.trading_account(args.trading_account)
        rules = s.venue_rules(args.trading_account, args.symbol)["symbols"][0]
        print(
            f"{account['name']} on {account['venue']}: {args.symbol} step {rules.get('stepSize')},"
            f" min notional {rules.get('minNotional')}"
        )
        # A held order expires at its own deadline (60 seconds by default) or after
        # 30 minutes, whichever is first, so give a person the ten minutes waited below.
        d = s.order(
            args.trading_account,
            args.symbol,
            args.side,
            args.size,
            price=args.price,
            valid_for=600,
        )
        print(
            f"decision {d['id']}: {d['verdict']}"
            + (f" ({d['reason']})" if d.get("reason") else "")
        )
        if d["verdict"] == "require_approval":
            print(
                "parked for a person: approve or reject it in the console (waiting up to 10 minutes)"
            )
            d = s.wait(d["id"], timeout=600, interval=2)
            print(f"person: {(d.get('approval') or {}).get('status')}")
        for e in d.get("evaluations", []):
            mark = "waived" if e.get("waived") else ("ok" if e["passed"] else "failed")
            print(
                f"  {e['ruleName']}: {e['observed']} vs {e['threshold']} {e['unit']} -> {mark}"
            )
        if d.get("venueAck"):
            print(f"venue: {d['venueAck']['status']}")


if __name__ == "__main__":
    main()
