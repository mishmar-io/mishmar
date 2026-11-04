// Screenshots for the docs page "Add an agent on Binance", from the console's mock engine.
// Usage: node scripts/shots-binance-agent.mjs ../../mishmar-console public/screens/binance/add-an-agent
import { spawn } from 'node:child_process'
import { mkdirSync } from 'node:fs'
import path from 'node:path'

const [consoleDir, outDir] = process.argv.slice(2)
const { chromium } = await import(path.resolve(consoleDir, 'node_modules/playwright/index.mjs'))
const PORT = 5184
const base = `http://localhost:${PORT}`
const ENGINE = 'https://api.testnet.mishmar.io/a/acc_2b7kq4xw9mzd/v1'
// An Ed25519 public key in the SPKI PEM `mishmar signer keygen` prints: the fixed 16-character
// prefix, then the key, made up here. The mock's is shorter than a real one.
const PEM_BODY = 'MCowBQYDK2VwAyEAh3Vq9cT1xZk7Lw2Rb8Ns4Fd6Gm0Jp5Yu3Ae7Ki1Oy9Wr2Q='
// Made up, in the shape Binance issues: 64 letters and digits.
const KEY_ID = 'Fz8Tq2Lw6Xc0Vb4Nm9Ks3Hd7Jg1Py5Re8Ua2Oi6Ew0Qt4Zl9Mx3Cn7Bv1Gh5Sk0A'
mkdirSync(outDir, { recursive: true })

const server = spawn('npx', ['vite', '--port', String(PORT), '--strictPort'], { cwd: consoleDir, stdio: 'ignore' })
async function waitForServer() {
  for (let i = 0; i < 60; i++) {
    try { if ((await fetch(base)).ok) return } catch {}
    await new Promise((r) => setTimeout(r, 500))
  }
  throw new Error('dev server did not come up')
}

try {
  await waitForServer()
  const browser = await chromium.launch()
  const context = await browser.newContext({ viewport: { width: 1360, height: 900 }, deviceScaleFactor: 2, colorScheme: 'dark' })
  await context.addInitScript(() => {
    localStorage.setItem('mishmar.token', 'screenshot-token')
    localStorage.setItem('mishmar.theme', 'dark')
  })
  const page = await context.newPage()
  await page.route('**/config.json', (r) => r.fulfill({ contentType: 'application/json', body: '{ "mode": "mock" }' }))
  page.on('pageerror', (e) => console.error('pageerror', e.message))

  // What the mock shows differently from the hosted tier: its engine address, short ids, its
  // short public key and the paper-mode check detail, which reads as Binance's answer in the
  // engine's words. The fresh secret is masked. Toasts and focus rings go.
  const tidy = () =>
    page.evaluate(([engine, pemBody]) => {
      if (!document.getElementById('docs-shots')) {
        const style = document.createElement('style')
        style.id = 'docs-shots'
        style.textContent = '[data-sonner-toaster] { display: none !important }'
        document.head.append(style)
      }
      const walk = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
      for (let n = walk.nextNode(); n; n = walk.nextNode()) {
        let t = n.textContent
          .replaceAll('http://mock.mishmar.local/v1', engine)
          .replace(/msk_[A-Za-z0-9_-]{12,}/g, 'msk_••••••••••••••••••••••••••••••••')
          .replace(/\bagt_[a-z0-9]{1,4}\b/g, 'agt_m4xq7rtd2kbn')
          .replace(/\bkey_[a-z0-9]{1,4}\b/g, 'key_2b7kq4xw9mzd_w8cn3pfj6tqa')
          .replace(/\bvk_[a-z0-9]{1,4}\b/g, 'vk_r5hd8kzm2vqe')
          .replaceAll('rt_bn_futures', 'ta_7d2kq9wmx4pb')
          .replace(/MCowBQYDK2VwAyEA[A-Za-z0-9+/]{27}=/g, pemBody)
        if (t.trim() === 'Paper mode: simulated') t = "Accepted by Binance, co-signed by the agent's signer"
        if (t !== n.textContent) n.textContent = t
      }
      // The mock's Bybit account is not one a reader has.
      for (const p of document.querySelectorAll('[role=dialog] p'))
        if (p.textContent.startsWith('Keys on ')) p.style.display = 'none'
    }, [ENGINE, PEM_BODY])
  const dialog = page.getByRole('dialog')
  const shot = async (name, locator = dialog) => {
    await page.waitForTimeout(400)
    await tidy()
    await locator.screenshot({ path: path.join(outDir, `${name}.png`), animations: 'disabled' })
    console.log('saved', name)
  }

  // 1. Agents → Add agent.
  await page.goto(`${base}/agents`)
  await page.getByRole('button', { name: 'Add agent' }).first().click()
  await page.getByLabel('Name').fill('BN reversion bot')
  await page.getByLabel('Description').fill('Mean reversion on BTCUSDT and ETHUSDT perps')
  await shot('add-agent')
  await dialog.getByRole('button', { name: /^(Add|Save|Create)/ }).last().click()
  await page.waitForURL(/\/agents\/agt_[^?]+\?setup=1/)
  const agentId = page.url().match(/agents\/(agt_[^?]+)/)[1]

  // 2. Setup: issue the key, set up the signer.
  await page.getByText('Issue its key').waitFor()
  await shot('setup-issue-key')
  await dialog.getByRole('button', { name: 'Issue key' }).click()
  await dialog.getByText('Copy it now').waitFor()
  await shot('setup-signer-init')
  await dialog.getByRole('button', { name: 'Done' }).click()

  // 3. A trading key on the Binance trading account.
  await dialog.getByRole('combobox', { name: 'Trading account' }).click()
  await page.getByRole('option', { name: /Binance futures/ }).click()
  await shot('setup-trading-key')
  await dialog.getByRole('button', { name: 'Add key' }).click()
  await dialog.getByText('Waiting for key generation').waitFor()
  await shot('setup-keygen')

  // 4. Key generation runs on the agent's machine; the mock is told it finished.
  await page.evaluate(async (a) => {
    const { db, completeKeygen } = await import('/src/mocks/db.ts')
    const key = db.agents.find((x) => x.id === a).venueKeys.find((k) => k.keygen === 'pending')
    completeKeygen(a, key.id)
  }, agentId)
  await dialog.getByRole('button', { name: 'Finish' }).waitFor({ timeout: 15000 })
  await shot('setup-done')
  await dialog.getByRole('button', { name: 'Finish' }).click()

  // 5. The Keys tab: the key's details, with the public key to register at Binance.
  // (In the app, not page.goto: a reload would start the mock over from its fixture.)
  await page.getByRole('tab', { name: 'Keys' }).click()
  await page.getByText('Trading keys').waitFor()
  await page.getByRole('button', { name: 'Show details of the key on Binance futures' }).click()
  const tradingKeys = page.locator('[data-slot="card"]').filter({ has: page.locator('[data-slot="card-title"]', { hasText: /^Trading keys/ }) })
  await page.evaluate(() => document.activeElement?.blur())
  await shot('key-details', tradingKeys)
  await page.getByRole('button', { name: 'Hide details of the key on Binance futures' }).click()

  // 6. The key id Binance issued.
  await tradingKeys.getByRole('button', { name: 'Actions' }).click()
  await page.getByRole('menuitem', { name: 'Register key id' }).click()
  await page.getByLabel('Key id issued by Binance').fill(KEY_ID)
  // The dialog traps the focus, so it goes to the dialog itself: no focus ring, no selection.
  await page.getByLabel('Key id issued by Binance').evaluate((el) => {
    el.setSelectionRange(0, 0)
    el.scrollLeft = 0
    el.closest('[role=dialog]').focus()
  })
  await shot('register-key-id')

  // 7. Saving verifies the key with Binance: it is active.
  await dialog.getByRole('button', { name: 'Save and verify' }).click()
  await dialog.waitFor({ state: 'detached' })
  await tradingKeys.getByText('Active').waitFor()
  await page.waitForTimeout(600)
  await tidy()
  await page.getByRole('tabpanel').screenshot({ path: path.join(outDir, 'agent-keys.png'), animations: 'disabled' })
  console.log('saved agent-keys')
  await browser.close()
} finally {
  server.kill()
}
