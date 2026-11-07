// The smallest bot: start the agent's signer, read the trading account and the
// venue's rules for a symbol, place one small limit order, wait if a person has
// to approve it, and print the verdict. The signer holds the agent's key and its
// half of the trading key; this process holds nothing.
//
//   npm install mishmar      # plus the mishmar binary on the PATH or in MISHMAR_BIN
//   node agent.ts ta_… BTC 0.001 50000

import { Signer } from "mishmar";

const [tradingAccountId, symbol, size, price] = process.argv.slice(2);
if (!tradingAccountId || !symbol || !size || !price) {
  console.error("usage: node agent.ts <trading account id> <symbol> <size> <limit price>");
  process.exit(2);
}

const s = await new Signer().start(); // ~/.mishmar/agent.json or $MISHMAR_AGENT_CONFIG
try {
  const account = await s.tradingAccount(tradingAccountId);
  const rules = ((await s.venueRules(tradingAccountId, symbol)).symbols as Record<string, unknown>[])[0];
  console.log(`${account.name} on ${account.venue}: ${symbol} step ${rules?.stepSize}, min notional ${rules?.minNotional}`);

  let d = await s.order({ tradingAccountId, symbol, side: "buy", size, price });
  console.log(`decision ${d.id}: ${d.verdict}${d.reason ? ` (${d.reason})` : ""}`);
  if (d.verdict === "require_approval") {
    console.log("parked for a person: approve or reject it in the console (waiting up to 10 minutes)");
    d = await s.wait(d.id, { timeoutMs: 600_000, intervalMs: 2_000 });
    console.log(`person: ${d.approval?.status}`);
  }
  for (const e of (d.evaluations ?? []) as Record<string, unknown>[]) {
    const mark = e.waived ? "waived" : e.passed ? "ok" : "failed";
    console.log(`  ${e.ruleName}: ${e.observed} vs ${e.threshold} ${e.unit} -> ${mark}`);
  }
  if (d.venueAck) console.log(`venue: ${(d.venueAck as Record<string, unknown>).status}`);
} finally {
  await s.stop();
}
