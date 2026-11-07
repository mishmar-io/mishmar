# The smallest bot (TypeScript)

Starts the agent's signer as a child process, reads the trading account and
the venue's rules for one symbol, places one small limit order, waits if a
person has to approve it, and prints the verdict with every rule that was
checked. This process holds no key.

```sh
npm install                          # the mishmar SDK; the mishmar binary must be on the PATH or in MISHMAR_BIN
node agent.ts ta_… BTC 0.001 50000   # trading account, symbol, size, limit price
```

Node 22.18 or later runs the `.ts` file as it is; on an older Node add
`--experimental-strip-types`. The symbol is `BTC` on Hyperliquid and
`BTCUSDT` on Binance. A price far from the market makes the order rest until
the proposal expires (60 seconds), so nothing fills. The signer's config is
`~/.mishmar/agent.json` or the file `MISHMAR_AGENT_CONFIG` names.
