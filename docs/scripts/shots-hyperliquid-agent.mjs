// Screenshots for the docs page "Add an agent on Hyperliquid", from the console's mock engine.
// Usage: node scripts/shots-hyperliquid-agent.mjs ../../mishmar-console public/screens/hyperliquid/add-an-agent
import { PAGE_VIEWPORT, literal, start } from './shots-common.mjs'

const PORT = 5184
const AGENT = 'agt_j2ropqqskmlf'
// Made up, in the shape of a Hyperliquid address: the API wallet key generation makes.
const API_WALLET = '0x7c2e9a41b8d3f05e6a9c1b2d4e8f7a3c5b0d9e61'

const { page, base, tidy, shot, mock, close } = await start({
  port: PORT,
  viewport: { width: 1360, height: 1400 },
})
try {
  await page.goto(`${base}/agents`)
  const dialog = page.getByRole('dialog')

  // 1. Agents → Add agent: a trade agent on the Hyperliquid account.
  await page.getByRole('button', { name: 'Add agent' }).first().click()
  await dialog.getByLabel('Name').fill('Trend follower')
  await dialog.getByRole('radio', { name: 'Trade' }).click()
  await dialog.getByRole('combobox', { name: 'Trades on' }).click()
  await page.getByRole('option', { name: /^Hyperliquid main/ }).click()
  await dialog.getByLabel('Description').fill('Trend following on BTC and ETH perps')
  await shot('add-agent')

  // 2. Added: its Mishmar API key (masked here) and the one command that starts it.
  await dialog.getByRole('button', { name: 'Add agent' }).click()
  await dialog.getByRole('heading', { name: 'Start the agent' }).waitFor()
  const agentId = await mock(
    (m) => m.db.agents.find((a) => a.name === 'Trend follower').id,
  )
  // The mock's short id, and the API wallet's name made from it, as a real id gives them.
  await tidy({
    subs: [
      [literal(`mishmar-${agentId.slice(-8)}`), 'g', `mishmar-${AGENT.slice(-8)}`],
      [`\\b${literal(agentId)}\\b`, 'g', AGENT],
    ],
  })
  await shot('start-the-agent')

  // The agent starts on its machine: its signer connects and runs key generation, which the
  // mock is told.
  const wallet = await mock((m, a) => {
    m.connectSigner(a)
    const key = m.db.agents.find((x) => x.id === a).venueKeys.find((k) => k.keygen === 'pending')
    return m.completeKeygen(a, key.id).publicKey
  }, agentId)
  await tidy({ subs: [[literal(wallet), 'g', API_WALLET]] })

  // 3. The API wallet to approve at Hyperliquid. The dialog asks every ten seconds whether it
  // is approved, and the mock says yes, so the shot comes first.
  await dialog.getByRole('heading', { name: 'Approve the API wallet at Hyperliquid' }).waitFor()
  await shot('approve-api-wallet')

  // 4. Approved: ready.
  await dialog.getByRole('heading', { name: /is ready$/ }).waitFor({ timeout: 20000 })
  await shot('ready')

  // Check it worked: the agent's Keys tab. (In the app, not page.goto: a reload would start
  // the mock over from its fixture.)
  await dialog.getByRole('button', { name: 'Finish' }).click()
  await page.waitForURL(/\/agents\/agt_/)
  await page.setViewportSize(PAGE_VIEWPORT)
  await page.getByRole('tab', { name: 'Keys' }).click()
  await page.getByRole('table', { name: 'Trading keys' }).getByText('Approved').waitFor()
  await page.waitForTimeout(600)
  await shot('agent-keys', page.getByRole('tabpanel'))
} finally {
  await close()
}
