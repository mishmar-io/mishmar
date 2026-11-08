# Examples

Bots that trade through Mishmar, to copy from. Each one runs the agent's
signer through the SDK's `Signer`, so the bot's own process never holds a
key: the signer holds the agent's key and its half of the trading key, and
Mishmar holds the other half. Every order goes to the engine as a proposal,
is checked against the owner's rules, and reaches the venue only when both
halves sign it.

Before running any of them: sign up on the hosted testnet at
https://app.testnet.mishmar.io, add a trading account and an agent
(https://docs.mishmar.io walks through both), and install the `mishmar`
binary on the machine the bot runs on (https://docs.mishmar.io/start/install-the-cli/).
The agent's Mishmar API key, shown once when the agent is added, is all the
signer needs: with it in `MISHMAR_API_KEY`, the first `mishmar signer run` (or
the SDK's `Signer`) writes the config and generates the agent's trading keys.

| Example | Shows | Language | Venue |
|---|---|---|---|
| [minimal-python](minimal-python/) | one limit order: the venue's rules, the decision, waiting for a person's approval | Python | Hyperliquid or Binance |
| [minimal-typescript](minimal-typescript/) | the same in TypeScript | TypeScript | Hyperliquid or Binance |
| [hl-trend-follower](hl-trend-follower/) | eight orders against seven rules: a resting order and its cancel, blocks, approvals, a reduce-only exit, then positions and PnL | Python | Hyperliquid testnet |
| [binance-reversion](binance-reversion/) | the same walk on Binance demo trading, optionally attached to a signer that runs on its own | Python | Binance demo trading |
