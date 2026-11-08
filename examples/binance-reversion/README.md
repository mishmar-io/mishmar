# Binance Reversion Agent

A test bot for a Binance demo-trading (USDⓈ-M futures) account. It walks
eight orders chosen against seven rules, states for each the verdict it
expects and the rules that must fail, and ends with a pass/fail table, the
agent's positions and its PnL for the day. It trades through the agent's
signer only and holds no key. Binance's minimum order is $50 on BTCUSDT and
$20 on ETHUSDT, which is why the sizes are larger than the Hyperliquid
example's.

## The rules

Add these in the console before the run, on the agent and on its portfolio.
The bot checks the decisions against these names, so keep them as they are.
The portfolio needs allocated capital for the leverage rule; the test used
$2,000.

| Scope | Name | Condition | Outcome |
|---|---|---|---|
| agent | Reversion max order | order > $600 | block |
| agent | Reversion order review | order > $250 | require approval |
| agent | Reversion agent gross | gross > $750 | block |
| portfolio | Reversion ETH review | ETH net > $100 | require approval |
| portfolio | Reversion portfolio gross | gross > $600 | block |
| portfolio | Reversion daily loss | daily loss > $100 | block |
| portfolio | Reversion leverage | leverage > 1 | block |

## The steps

1. A limit buy of about $60 of BTCUSDT 3 % below the market rests, then
2. is cancelled.
3. A $110 market buy of BTCUSDT fills.
4. A $150 market buy of ETHUSDT parks for a person (ETH review).
5. A $700 market buy of BTCUSDT is blocked (max order, order review, and the gross rules it would cross).
6. A $300 market buy of BTCUSDT parks for a person (order review) and fills once approved.
7. A $240 market buy of BTCUSDT is blocked by the portfolio's gross cap, or allowed if the gross is still under it.
8. A reduce-only market sell of the whole BTCUSDT position fills and the account is flat.

`--sequence cap` runs a shorter walk with no approval to wait for: two $200
buys under the review threshold, a $240 buy that crosses the portfolio's
gross cap, and the exit.

## Run it

1. Sign up at https://app.testnet.mishmar.io, add a Binance demo-trading
   trading account and an agent
   (https://docs.mishmar.io/binance/add-a-trading-account/ and
   https://docs.mishmar.io/binance/add-an-agent/), and install the
   `mishmar` binary (https://docs.mishmar.io/start/install-the-cli/). Put the
   agent's Mishmar API key, shown once when the agent is added, in
   `MISHMAR_API_KEY`: the signer writes its config from it the first time it
   runs and generates the agent's trading key.
2. Add the rules above.
3. Install the SDK and run the bot:

   ```sh
   pip install -r requirements.txt
   python agent.py --trading-account ta_… --results results.json
   ```

   `--config ~/.mishmar/agents/<name>.json` when the agent's config is not at
   the default path, `--only 3,8` to run some of the steps,
   `--approval-timeout` (seconds, default 900) for how long a parked step
   waits.

   To keep one signer up across runs (and let it answer the console's key
   verification at the same time), run it on its own and attach to it:

   ```sh
   mishmar signer run --local unix:/tmp/mishmar-binance.sock &
   python agent.py --connect unix:/tmp/mishmar-binance.sock --trading-account ta_…
   ```

Steps 4 and 6 wait for you: approve or reject them in the console. The exit
code is 0 when every step passed.

## What it prints

For each step, the intent, the decision with every rule the engine checked,
what the signer and the venue said, and PASS or FAIL:

```
== 3. $110 market buy BTCUSDT
   intent: order buy 0.001 BTCUSDT market
   dec_…: ALLOW
   notional $110.40 at -; cloid msh-…; engine 4.1 ms; round trip 530 ms
     [agent    ] Reversion max order        order_size          110.40 vs    600 usd   -> ok
     [agent    ] Reversion order review     order_size          110.40 vs    250 usd   -> ok
     [agent    ] Reversion agent gross      gross_exposure      110.40 vs    750 usd   -> ok
     [portfolio] Reversion ETH review       net_exposure             0 vs    100 usd   -> ok
     [portfolio] Reversion portfolio gross  gross_exposure      110.40 vs    600 usd   -> ok
     [portfolio] Reversion daily loss       daily_loss               0 vs    100 usd   -> ok
     [portfolio] Reversion leverage         leverage              0.06 vs      1 ratio -> ok
   signer: signed
   venue: {"status": "filled", "orderId": …}
   PASS
```

Then the table, the positions and the PnL, and everything the run saw goes
to `--results` as JSON.
