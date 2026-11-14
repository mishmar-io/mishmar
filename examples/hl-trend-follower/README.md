# HL Trend Follower

A test bot for a Hyperliquid testnet connected account. It walks eight orders
chosen against seven rules, states for each the verdict it expects and the
rules that must fail, and ends with a pass/fail table, the agent's positions
and its PnL for the day. It trades through the agent's signer only: the SDK
runs `mishmar signer run --local` as a child of this process, which holds no
key.

## The rules

Add these in the console before the run: four on the agent, its own limits,
and three on the connected account it trades on, which hold for anything
that trades there. The bot checks the decisions against these names, so keep
them as they are. Give the agent $500 of allocated capital first: its
leverage cap is measured against that, and Mishmar refuses to turn the rule
on while the agent has none.

| Scope | Name | Condition | Outcome |
|---|---|---|---|
| agent | Max order size | order > $150 | block |
| agent | Review large orders | order > $40 | require approval |
| agent | Agent gross exposure | gross > $150 | block |
| agent | Leverage cap | leverage > 1 | block |
| connected account | ETH exposure review | ETH net > $30 | require approval |
| connected account | Account gross exposure | gross > $100 | block |
| connected account | Daily loss stop | daily loss > $25 | block |

The account's rules see every position on the account, whoever opened it,
and the bot predicts their verdicts from its own positions: run it on a
testnet account that nothing else trades on during the run.

## The steps

1. A small limit buy of BTC 3 % below the market rests, then
2. is cancelled.
3. A $20 market buy of BTC fills.
4. A $35 market buy of ETH parks for a person (ETH exposure review).
5. A $200 market buy of BTC is blocked (max order size, and the exposure rules it would also cross).
6. A $60 market buy of BTC parks for a person (large-order review) and fills once approved.
7. A $30 market buy of BTC is blocked by the account's gross cap, or allowed if the gross is still under it.
8. A reduce-only market sell of the whole BTC position fills and the account is flat.

## Run it

1. Sign up at https://app.testnet.mishmar.io, add a Hyperliquid testnet
   connected account and an agent
   (https://docs.mishmar.io/hyperliquid/add-a-connected-account/ and
   https://docs.mishmar.io/hyperliquid/add-an-agent/), and install the
   `mishmar` binary (https://docs.mishmar.io/start/install-the-cli/). Put the
   agent's Mishmar API key, shown once when the agent is added, in
   `MISHMAR_API_KEY`: the signer writes its config from it the first time it
   runs and generates the agent's trading key.
2. Add the rules above.
3. Install the SDK and run the bot:

   ```sh
   pip install -r requirements.txt
   python agent.py --connected-account ta_… --results results.json
   ```

   `--config ~/.mishmar/agents/<name>.json` when the agent's config is not at
   the default path, `--only 3,8` to run some of the steps, and
   `--approval-timeout` (seconds, default 900) for how long a parked step
   waits.

Steps 4 and 6 wait for you: approve or reject them in the console. The exit
code is 0 when every step passed.

## What it prints

For each step, the intent, the decision with every rule the engine checked,
what the signer and the venue said, and PASS or FAIL:

```
== 3. $20 market buy BTC
   intent: order buy 0.00019 BTC market
   dec_…: ALLOW
   notional $20.12 at -; cloid msh-…; engine 3.9 ms; round trip 412 ms
     [agent            ] Max order size             order_size           20.12 vs    150 usd   -> ok
     [agent            ] Review large orders        order_size           20.12 vs     40 usd   -> ok
     [agent            ] Agent gross exposure       gross_exposure       20.12 vs    150 usd   -> ok
     [agent            ] Leverage cap               leverage              0.04 vs      1 ratio -> ok
     [connected_account] ETH exposure review        net_exposure             0 vs     30 usd   -> ok
     [connected_account] Account gross exposure     gross_exposure       20.12 vs    100 usd   -> ok
     [connected_account] Daily loss stop            daily_loss               0 vs     25 usd   -> ok
   signer: signed
   venue: filled order 1234…
   PASS
```

Then the table, the positions and the PnL, and everything the run saw goes
to `--results` as JSON (the decisions, the positions, the PnL and the
agent's decision log).

```
== result
   PASS  1. small passive limit buy rests
   PASS  2. cancel the resting order
   PASS  3. $20 market buy BTC
   PASS  4. $35 market buy ETH (account ETH review)  (rejected)
   PASS  5. $200 market buy BTC (agent max order)
   PASS  6. $60 market buy BTC (agent large-order review)  (approved)
   PASS  7. $30 market buy BTC (account gross cap)
   PASS  8. reduce-only exit of the whole BTC position
```
