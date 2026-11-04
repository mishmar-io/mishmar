// Screenshots for the docs page "Add a Binance trading account", from the console's mock engine.
// Usage: node scripts/shots-binance-trading-account.mjs ../../mishmar-console public/screens/binance/add-a-trading-account
import { spawn } from 'node:child_process'
import { mkdirSync } from 'node:fs'
import path from 'node:path'

const [consoleDir, outDir] = process.argv.slice(2)
const { chromium } = await import(path.resolve(consoleDir, 'node_modules/playwright/index.mjs'))
const PORT = 5183
const base = `http://localhost:${PORT}`
// Made up, in the shape Binance issues: 64 letters and digits. The secret field shows dots.
const READ_KEY = 'q7Xk2Rm9VbT4wLp8Nz3HcY6sJd1Fg5Ue0Ka7Qo2Wi9Mx4Bt8Ev3Ry6Cn1Pl5Sh0Z'
const READ_SECRET = 'not-a-real-secret-0000000000000000000000000000000000000000000000'
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

  // What the mock shows differently from the hosted tier: short ids, and the paper-mode check
  // details, which read as Binance demo trading's answers, in the engine's words. Toasts go.
  const tidy = () =>
    page.evaluate(() => {
      if (!document.getElementById('docs-shots')) {
        const style = document.createElement('style')
        style.id = 'docs-shots'
        style.textContent = '[data-sonner-toaster] { display: none !important }'
        document.head.append(style)
      }
      const answers = ['Accepted by Binance', '$4891.37 available', 'One-way']
      const walk = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
      let simulated = 0
      for (let n = walk.nextNode(); n; n = walk.nextNode()) {
        let t = n.textContent.replace(/\bta_[a-z0-9]{1,4}\b/g, 'ta_7d2kq9wmx4pb')
        if (t.trim() === 'Paper mode: simulated') t = answers[simulated++ % answers.length]
        // The environment check, as the engine words it once verified.
        if (t.trim() === 'demo' && n.parentElement?.closest('li')) t = 'Binance demo trading'
        if (t !== n.textContent) n.textContent = t
      }
    })
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

  // 2. The exchange account: name and its read-only key.
  await dialog.getByRole('button', { name: 'Binance' }).click()
  await page.getByLabel('Name').fill('Binance futures')
  await page.getByLabel('API key').fill(READ_KEY)
  await page.getByLabel('Secret key').fill(READ_SECRET)
  await page.evaluate(() => document.activeElement?.blur())
  await shot('read-only-key')

  // 3. Added: verify it.
  await dialog.getByRole('button', { name: 'Add account' }).click()
  await dialog.getByRole('button', { name: 'Verify now' }).waitFor()
  await dialog.getByRole('button', { name: 'Verify now' }).click()
  await dialog.getByRole('button', { name: 'Verify again' }).waitFor()
  await page.evaluate(() => document.activeElement?.blur())
  await shot('verified')

  // 4. Done: the trading account's page, its Setup and Verification cards.
  await dialog.getByRole('button', { name: 'Done' }).click()
  await page.waitForURL(/\/trading-accounts\/ta_/)
  await page.getByText('Who trades here').waitFor()
  const card = (title) =>
    page.locator('[data-slot="card"]').filter({ has: page.locator('[data-slot="card-title"]', { hasText: title }) })
  await shot('setup-card', card(/^Setup$/))
  await shot('verification-card', card(/^Verification/))
  await browser.close()
} finally {
  server.kill()
}
