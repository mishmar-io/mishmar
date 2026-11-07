# The smallest bot (Python)

Starts the agent's signer as a child process, reads the trading account and
the venue's rules for one symbol, places one small limit order, waits if a
person has to approve it, and prints the verdict with every rule that was
checked. This process holds no key.

```sh
pip install -r requirements.txt      # the mishmar SDK; the mishmar binary must be on the PATH or in MISHMAR_BIN
python agent.py --trading-account ta_… --symbol BTC --size 0.001 --price 50000
```

`--symbol` is `BTC` on Hyperliquid and `BTCUSDT` on Binance. A price far from
the market makes the order rest until the proposal expires (60 seconds), so
nothing fills. `--config` names the signer's config when it is not at
`~/.mishmar/agent.json`.
