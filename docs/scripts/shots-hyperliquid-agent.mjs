// Screenshots for the docs page "Add an agent on Hyperliquid", from the console's mock engine.
// Usage: node scripts/shots-hyperliquid-agent.mjs ../../mishmar-console public/screens/hyperliquid/add-an-agent
import { spawn } from 'node:child_process'
import { mkdirSync } from 'node:fs'
import path from 'node:path'

const [consoleDir, outDir] = process.argv.slice(2)
const { chromium } = await import(path.join(consoleDir, 'node_modules/playwright/index.mjs'))
const PORT = 5181
const base = `http://localhost:${PORT}`
const ENGINE = 'https://api.mishmar.io/a/acc_2b7kq4xw9mzd/v1'
const MASTER = '0x4f3a9b2c7d1e0f6a5b4c3d2e1f0a9b8c7d6e5f4a'
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
  // A browser wallet holding the trading account's master address, on Arbitrum Sepolia.
  await context.addInitScript((master) => {
    window.ethereum = {
      request: async ({ method }) => {
        await new Promise((r) => setTimeout(r, 150))
        if (method === 'eth_requestAccounts' || method === 'eth_accounts') return [master]
        if (method === 'eth_chainId') return '0x66eee'
        if (method === 'eth_signTypedData_v4') return '0x' + 'ab'.repeat(64) + '1b'
        throw new Error('unsupported ' + method)
      },
      on() {}, removeListener() {},
    }
  }, MASTER)
  const page = await context.newPage()
  await page.route('**/config.json', (r) => r.fulfill({ contentType: 'application/json', body: '{ "mode": "mock" }' }))
  page.on('pageerror', (e) => console.error('pageerror', e.message))

  // What the mock shows differently from the hosted tier: its engine address, short ids and the
  // paper-mode check details, which read as the venue's answer with the key's 100-day expiry.
  // The fresh secret is masked. Toasts and focus rings go.
  const until = new Date(Date.now() + 100 * 86400000).toISOString().replace(/\.\d+Z$/, 'Z')
  const tidy = () =>
    page.evaluate(([engine, until]) => {
      if (!document.getElementById('docs-shots')) {
        const style = document.createElement('style')
        style.id = 'docs-shots'
        style.textContent = '[data-sonner-toaster] { display: none !important }'
        document.head.append(style)
      }
      const walk = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
      let simulated = 0
      for (let n = walk.nextNode(); n; n = walk.nextNode()) {
        let t = n.textContent
          .replaceAll('http://mock.mishmar.local/v1', engine)
          .replace(/msk_[A-Za-z0-9_-]{12,}/g, 'msk_••••••••••••••••••••••••••••••••')
          .replace(/\bagt_[a-z0-9]{1,4}\b/g, 'agt_j2ropqqskmlf')
          .replace(/\bkey_[a-z0-9]{1,4}\b/g, 'key_2b7kq4xw9mzd_h3kq8vz2m4pt')
          .replace(/\bvk_[a-z0-9]{1,4}\b/g, 'vk_t2w6oobpwgkw')
          .replaceAll('rt_hl_main', 'ta_2xvmaf3cmvxw')
        if (t.trim() === 'Paper mode: simulated')
          t = simulated++ === 0 ? `Approved as mishmar-pqqskmlf, valid until ${until}` : 'testnet'
        if (t !== n.textContent) n.textContent = t
      }
      // The mock's Bybit account is not one a reader has.
      for (const p of document.querySelectorAll('[role=dialog] p'))
        if (p.textContent.startsWith('Keys on ')) p.style.display = 'none'
    }, [ENGINE, until])
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
  await page.getByLabel('Name').fill('HL trend bot')
  await page.getByLabel('Description').fill('Trend following on BTC and ETH perps')
  await shot('add-agent')
  await dialog.getByRole('button', { name: /^(Add|Save|Create)/ }).last().click()
  await page.waitForURL(/\/agents\/agt_[^?]+\?setup=1/)
  const agentId = page.url().match(/agents\/(agt_[^?]+)/)[1]

  // 2. Setup: issue the key.
  await page.getByText('Issue its key').waitFor()
  await shot('setup-issue-key')
  await dialog.getByRole('button', { name: 'Issue key' }).click()
  await dialog.getByText('Copy it now').waitFor()
  await shot('setup-signer-init')
  await dialog.getByRole('button', { name: 'Done' }).click()

  // 3. A trading key on the Hyperliquid trading account.
  await dialog.getByRole('combobox', { name: 'Trading account' }).click()
  await page.getByRole('option', { name: /Hyperliquid main/ }).click()
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
  await dialog.getByRole('button', { name: 'Approve with wallet' }).waitFor({ timeout: 15000 })
  await shot('setup-approve')

  // 5. The master wallet approves the API wallet.
  await dialog.getByRole('button', { name: 'Approve with wallet' }).click()
  const sheet = page.getByRole('dialog')
  await sheet.getByRole('button', { name: 'Approve with wallet' }).waitFor()
  await sheet.getByRole('button', { name: 'Approve with wallet' }).click()
  await sheet.getByText(/Approved\./).waitFor({ timeout: 15000 })
  await page.waitForTimeout(400)
  await tidy()
  const box = await sheet.locator('section[aria-label="Approve API wallet"]').boundingBox()
  await page.screenshot({ path: path.join(outDir, 'approved.png'), animations: 'disabled', clip: { x: box.x - 20, y: box.y - 20, width: box.width + 40, height: box.height + 40 } })
  console.log('saved approved')
  await page.keyboard.press('Escape')

  // 6. The agent's Keys tab: the identity key and the trading key.
  // (In the app, not page.goto: a reload would start the mock over from its fixture.)
  await page.getByRole('tab', { name: 'Keys' }).click()
  await page.getByText('Trading keys').waitFor()
  await page.waitForTimeout(600)
  await tidy()
  // The two key panels, without the shell around them.
  await page.getByRole('tabpanel').screenshot({ path: path.join(outDir, 'agent-keys.png'), animations: 'disabled' })
  console.log('saved agent-keys')
  await browser.close()
} finally {
  server.kill()
}
