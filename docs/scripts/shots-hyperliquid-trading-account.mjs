// Screenshots for the docs page "Add a Hyperliquid trading account", from the console's mock engine.
// Usage: node scripts/shots-hyperliquid-trading-account.mjs ../../mishmar-console public/screens/hyperliquid/add-a-trading-account
import { spawn } from 'node:child_process'
import { mkdirSync } from 'node:fs'
import path from 'node:path'

const [consoleDir, outDir] = process.argv.slice(2)
const { chromium } = await import(path.resolve(consoleDir, 'node_modules/playwright/index.mjs'))
const PORT = 5182
const base = `http://localhost:${PORT}`
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
  // A browser wallet holding the master address, for Use my wallet.
  await context.addInitScript((master) => {
    window.ethereum = {
      request: async ({ method }) => {
        await new Promise((r) => setTimeout(r, 150))
        if (method === 'eth_requestAccounts' || method === 'eth_accounts') return [master]
        if (method === 'eth_chainId') return '0x66eee'
        throw new Error('unsupported ' + method)
      },
      on() {}, removeListener() {},
    }
  }, MASTER)
  const page = await context.newPage()
  await page.route('**/config.json', (r) => r.fulfill({ contentType: 'application/json', body: '{ "mode": "mock" }' }))
  page.on('pageerror', (e) => console.error('pageerror', e.message))

  // What the mock shows differently from the hosted tier: short ids, and the paper-mode check
  // details, which read as Hyperliquid's answers (the master address, its margin). Toasts go.
  const tidy = () =>
    page.evaluate((master) => {
      if (!document.getElementById('docs-shots')) {
        const style = document.createElement('style')
        style.id = 'docs-shots'
        style.textContent = '[data-sonner-toaster] { display: none !important }'
        document.head.append(style)
      }
      const walk = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
      let simulated = 0
      for (let n = walk.nextNode(); n; n = walk.nextNode()) {
        let t = n.textContent.replace(/\bta_[a-z0-9]{1,4}\b/g, 'ta_2xvmaf3cmvxw')
        if (t.trim() === 'Paper mode: simulated') t = simulated++ === 0 ? master : '$1,249.62 available'
        if (t !== n.textContent) n.textContent = t
      }
    }, MASTER)
  const dialog = page.getByRole('dialog')
  const shot = async (name, locator = dialog) => {
    await page.waitForTimeout(400)
    await tidy()
    await locator.screenshot({ path: path.join(outDir, `${name}.png`), animations: 'disabled' })
    console.log('saved', name)
  }

  // 1. Trading accounts → Add trading account: the venue.
  await page.goto(`${base}/trading-accounts`)
  await page.getByRole('button', { name: 'Add trading account' }).first().click()
  await dialog.getByText('Pick the venue.', { exact: false }).waitFor()
  await shot('pick-venue')

  // 2. The master account: name, address from the wallet, the network the engine trades.
  await dialog.getByRole('button', { name: 'Hyperliquid' }).click()
  await page.getByLabel('Name').fill('Hyperliquid main')
  await dialog.getByRole('button', { name: 'Use my wallet' }).click()
  await page.waitForFunction((m) => document.getElementById('trading-account-master')?.value === m, MASTER)
  await dialog.getByText('Testnet').waitFor()
  // The focus ring on the address field goes.
  await page.evaluate(() => document.activeElement?.blur())
  await shot('master-account')

  // 3. Added: verify it.
  await dialog.getByRole('button', { name: 'Add account' }).click()
  await dialog.getByRole('button', { name: 'Verify now' }).waitFor()
  await dialog.getByRole('button', { name: 'Verify now' }).click()
  await dialog.getByRole('button', { name: 'Verify again' }).waitFor()
  await page.evaluate(() => document.activeElement?.blur())
  await shot('verified')

  // 4. Done: the trading account's page, its Verification card.
  await dialog.getByRole('button', { name: 'Done' }).click()
  await page.waitForURL(/\/trading-accounts\/ta_/)
  await page.getByText('Who trades here').waitFor()
  const card = page.locator('[data-slot="card"]').filter({ has: page.locator('[data-slot="card-title"]', { hasText: /^Verification/ }) })
  await shot('verification-card', card)
  await browser.close()
} finally {
  server.kill()
}
