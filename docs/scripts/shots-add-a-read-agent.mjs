// Screenshots for the docs page "Add a read agent", from the console's mock engine.
// Usage: node scripts/shots-add-a-read-agent.mjs ../../mishmar-console public/screens/start/add-a-read-agent
import { PAGE_VIEWPORT, literal, start } from './shots-common.mjs'

const PORT = 5186
const AGENT = 'agt_w7kd3nqx5tbe'
// The fixture's two accounts, by the ids a real account has.
const ACCOUNTS = [
  ['rt_hl_main', 'ta_2xvmaf3cmvxw'],
  ['rt_bn_futures', 'ta_7d2kq9wmx4pb'],
]

const { page, base, tidy, shot, mock, card, close } = await start({
  port: PORT,
  viewport: { width: 1360, height: 1400 },
})
try {
  await page.goto(`${base}/agents`)
  // The mock's Bybit account is not one a reader has: Mishmar trades Hyperliquid and Binance.
  await tidy({
    subs: ACCOUNTS.map(([mock, real]) => [`\\b${mock}\\b`, 'g', real]),
    hide: [['[role=dialog] label', 'Bybit']],
  })
  const dialog = page.getByRole('dialog')

  // 1. Agents → Add agent: read only, on both accounts.
  await page.getByRole('button', { name: 'Add agent' }).first().click()
  await dialog.getByLabel('Name').fill('Desk assistant')
  await dialog.getByRole('radio', { name: 'Read only' }).click()
  await dialog.getByRole('checkbox', { name: /Hyperliquid main/ }).click()
  await dialog.getByRole('checkbox', { name: /Binance futures/ }).click()
  await dialog.getByLabel('Description').fill('Answers questions about the accounts')
  await shot('add-agent')

  // 2. Added: its Mishmar API key, shown once (masked here).
  await dialog.getByRole('button', { name: 'Add agent' }).click()
  await dialog.getByRole('heading', { name: 'Desk assistant is ready' }).waitFor()
  const agentId = await mock((m) => m.db.agents.find((a) => a.name === 'Desk assistant').id)
  await tidy({ subs: [[`\\b${literal(agentId)}\\b`, 'g', AGENT]] })
  await shot('key')

  // Check it worked: the agent's page, read only, with its accounts.
  // (In the app, not page.goto: a reload would start the mock over from its fixture.)
  await dialog.getByRole('button', { name: 'Finish' }).click()
  await page.waitForURL(/\/agents\/agt_/)
  await page.setViewportSize(PAGE_VIEWPORT)
  await card(/^Connected accounts/).getByText('Binance futures').waitFor()
  await page.waitForTimeout(600)
  await shot('agent-page', [card(/^Overview$/), card(/^Connected accounts/)])

  // The program reads, which the mock is told: its access log, as the engine writes it.
  await mock((m, a) => {
    const agent = m.db.agents.find((x) => x.id === a)
    const base = { agentId: a, agentName: agent.name, keyId: agent.keys[0].id }
    const at = (min) => new Date(Date.now() - min * 60_000).toISOString()
    const reads = [
      [9, 'accounts'],
      [8, 'balances', 'rt_hl_main'],
      [8, 'positions', 'rt_hl_main'],
      [7, 'balances', 'rt_bn_futures'],
      [6, 'transactions', 'rt_bn_futures', 7 * 24 * 60],
      [4, 'equity', 'rt_hl_main', 30 * 24 * 60],
    ]
    for (const [min, read, account, span] of reads)
      m.logAccess({
        ...base,
        at: at(min),
        read,
        ...(account ? { connectedAccountId: account } : {}),
        ...(span ? { from: at(min + span), to: at(min) } : {}),
      })
  }, agentId)
  await page.getByRole('tab', { name: 'Access' }).click()
  await card('What it read').getByText('Value over time').waitFor()
  await page.waitForTimeout(400)
  await shot('agent-access', card('What it read'))
} finally {
  await close()
}
