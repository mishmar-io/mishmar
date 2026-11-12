// Screenshots for the docs page "Add an agent on Binance", from the console's mock engine.
// Usage: node scripts/shots-binance-agent.mjs ../../mishmar-console public/screens/binance/add-an-agent
import { PAGE_VIEWPORT, literal, start } from './shots-common.mjs'

const PORT = 5185
const AGENT = 'agt_m4xq7rtd2kbn'
// An Ed25519 public key as an SPKI PEM, made up here: the mock's is shorter than a real one.
const PUBLIC_KEY =
  '-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEAjzwanlLXsEbh+KPCnV57EEpsL56D0bV6DkyW8tihO3U=\n-----END PUBLIC KEY-----'
// Made up, in the shape Binance issues: 64 letters and digits.
const KEY_ID = 'Fz8Tq2Lw6Xc0Vb4Nm9Ks3Hd7Jg1Py5Re8Ua2Oi6Ew0Qt4Zl9Mx3Cn7Bv1Gh5Sk0A'

const { page, base, tidy, shot, mock, close } = await start({
  port: PORT,
  viewport: { width: 1360, height: 1400 },
})
try {
  await page.goto(`${base}/agents`)
  const dialog = page.getByRole('dialog')

  // 1. Agents → Add agent: a trade agent on the Binance account.
  await page.getByRole('button', { name: 'Add agent' }).first().click()
  await dialog.getByLabel('Name').fill('Reversion')
  await dialog.getByRole('radio', { name: 'Trade' }).click()
  await dialog.getByRole('combobox', { name: 'Trades on' }).click()
  await page.getByRole('option', { name: /^Binance futures/ }).click()
  await dialog.getByLabel('Description').fill('Mean reversion on BTCUSDT and ETHUSDT perps')
  await shot('add-agent')

  // 2. Added: its Mishmar API key (masked here) and the one command that starts it.
  await dialog.getByRole('button', { name: 'Add agent' }).click()
  await dialog.getByRole('heading', { name: 'Start the agent' }).waitFor()
  const agentId = await mock((m) => m.db.agents.find((a) => a.name === 'Reversion').id)
  await tidy({ subs: [[`\\b${literal(agentId)}\\b`, 'g', AGENT]] })
  await shot('start-the-agent')

  // The agent starts on its machine: its signer connects and runs key generation, which the
  // mock is told.
  const publicKey = await mock((m, a) => {
    m.connectSigner(a)
    const key = m.db.agents.find((x) => x.id === a).venueKeys.find((k) => k.keygen === 'pending')
    return m.completeKeygen(a, key.id).publicKey
  }, agentId)
  await tidy({ subs: [[literal(publicKey), 'g', PUBLIC_KEY]] })

  // 3. The public key to register at Binance, and the key id Binance issued for it.
  await dialog.getByRole('heading', { name: 'Register the key at Binance' }).waitFor()
  await dialog.getByLabel('Key id issued by Binance').fill(KEY_ID)
  await dialog.getByLabel('Key id issued by Binance').evaluate((el) => {
    el.setSelectionRange(0, 0)
    el.scrollLeft = 0
  })
  await shot('register-key')

  // 4. Save and verify: the key is active, the agent ready.
  await dialog.getByRole('button', { name: 'Save and verify' }).click()
  await dialog.getByRole('heading', { name: /is ready$/ }).waitFor({ timeout: 20000 })
  await shot('ready')

  // Check it worked: the agent's Keys tab. (In the app, not page.goto: a reload would start
  // the mock over from its fixture.)
  await dialog.getByRole('button', { name: 'Finish' }).click()
  await page.waitForURL(/\/agents\/agt_/)
  await page.setViewportSize(PAGE_VIEWPORT)
  await page.getByRole('tab', { name: 'Keys' }).click()
  await page.getByRole('table', { name: 'Trading keys' }).getByText('Key id entered').waitFor()
  await page.waitForTimeout(600)
  await shot('agent-keys', page.getByRole('tabpanel'))
} finally {
  await close()
}
