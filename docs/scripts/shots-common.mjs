// What every screenshot script shares: the console's dev server on its own port, a dark 2x
// browser on the console's in-page mock engine, a tidy pass that keeps rewriting what only the
// mock shows, and a shot cropped to one panel or dialog, or to the box around several.
// Each script runs as: node scripts/shots-<page>.mjs <console checkout> <output folder>
import { spawn } from 'node:child_process'
import { mkdirSync } from 'node:fs'
import path from 'node:path'

/** The placeholder a masked Mishmar API key shows in a screenshot. */
export const MASKED_KEY = 'msk_' + '•'.repeat(32)

/**
 * The window for a console page's panels: narrow enough that they stack in one column about as
 * wide as the docs' text, tall enough that a page needs no scrolling.
 */
export const PAGE_VIEWPORT = { width: 980, height: 2000 }

/** A string as a pattern for `tidy`'s `subs`, matching it exactly. */
export const literal = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

/**
 * The rewrites every page needs: a fresh Mishmar API key masked, the mock's engine taken out
 * of the start command (a hosted key names its own), and the read-only key fingerprint the
 * mock makes from hex text, which reads as fake, in the shape a real one has.
 */
export const COMMON_SUBS = [
  ['msk_[0-9a-f]{6}_[0-9a-f]{32}', 'g', MASKED_KEY],
  [' MISHMAR_ENGINE=http://localhost:8080/v1', 'g', ''],
  ['SHA256:[A-Za-z0-9+/]{43}', 'g', 'SHA256:n4Qe8Lr1Vx6Tz0Kb3Hp9Wd2Ys7Fm5Ja8Uc1Gi4Ot6Rk'],
]

/**
 * Starts the console on `port` and a browser on it. `wallet` puts a browser wallet holding that
 * address in the page. Returns the page, the console's address, `tidy` (more rewrites, merged
 * into what is there), `shot`, `mock` (the in-page mock's state), `card` (a panel by its title)
 * and `close`, which stops the browser and the server.
 */
export async function start({ port, viewport = { width: 1360, height: 900 }, wallet }) {
  const [consoleDir, outDir] = process.argv.slice(2)
  if (!consoleDir || !outDir) {
    console.error('usage: node scripts/shots-<page>.mjs <mishmar-console checkout> <output folder>')
    process.exit(2)
  }
  mkdirSync(outDir, { recursive: true })
  const base = `http://localhost:${port}`
  // A server already there would answer for the wrong checkout: refuse to start.
  if (await answers(base)) throw new Error(`port ${port} is taken; free it or change PORT`)
  const { chromium } = await import(path.resolve(consoleDir, 'node_modules/playwright/index.mjs'))
  const server = spawn(
    process.execPath,
    ['node_modules/vite/bin/vite.js', '--port', String(port), '--strictPort'],
    { cwd: consoleDir, stdio: 'ignore' },
  )
  let browser
  async function close() {
    await browser?.close().catch(() => {})
    server.kill()
  }
  try {
    for (let i = 0; !(await answers(base)); i++) {
      if (i > 60 || server.exitCode !== null) throw new Error('the dev server did not come up')
      await new Promise((r) => setTimeout(r, 500))
    }
    browser = await chromium.launch()
    // UTC, so times read the same wherever the shots are taken, and no range crosses a clock change.
    const context = await browser.newContext({
      viewport,
      deviceScaleFactor: 2,
      colorScheme: 'dark',
      timezoneId: 'UTC',
    })
    await context.addInitScript(() => {
      localStorage.setItem('mishmar.token', 'screenshot-token')
      localStorage.setItem('mishmar.theme', 'dark')
    })
    if (wallet) await context.addInitScript(fakeWallet, wallet)
    await context.addInitScript(installTidy, { subs: COMMON_SUBS })
    const page = await context.newPage()
    // Always the in-page mock, whatever public/config.json says in that checkout.
    await page.route('**/config.json', (r) =>
      r.fulfill({ contentType: 'application/json', body: '{ "mode": "mock" }' }),
    )
    page.on('pageerror', (e) => console.error('pageerror', e.message))

    /** Adds rewrites (see installTidy): at once, and on every page loaded after. */
    async function tidy(cfg) {
      await context.addInitScript((c) => window.__shots.add(c), cfg)
      await page.evaluate((c) => window.__shots.add(c), cfg)
    }

    /**
     * Saves `<name>.png`, cropped to `locator` (the open dialog by default), or to the box
     * around several. No hover, no focus ring, no toast.
     */
    async function shot(name, locator = page.getByRole('dialog')) {
      await page.mouse.move(0, 0)
      await page.evaluate(() => {
        const a = document.activeElement
        if (!a || a === document.body) return
        const dialog = a.closest('[role=dialog]')
        if (dialog && dialog !== a) dialog.focus()
        else a.blur()
      })
      await page.waitForTimeout(400)
      // A dialog whose step changed re-centres itself: wait until it stands still.
      for (let i = 0, last = ''; i < 20; i++) {
        const now = JSON.stringify(await Promise.all([locator].flat().map((l) => l.boundingBox())))
        if (now === last) break
        last = now
        await page.waitForTimeout(150)
      }
      await page.evaluate(() => window.__shots.apply())
      const file = path.join(outDir, `${name}.png`)
      // Cropped from the window, not by the element's own screenshot, which rounds a box that
      // sits on a half pixel outward and takes in a row of what is behind it. Snapped inward to
      // the 2x pixel grid instead.
      await page.evaluate(() => scrollTo(0, 0))
      const boxes = await Promise.all(
        [locator].flat().map((l) =>
          l.evaluate((el) => {
            const r = el.getBoundingClientRect()
            return [r.left, r.top, r.right, r.bottom, el.scrollHeight > el.clientHeight + 1]
          }),
        ),
      )
      if (boxes.some((b) => b[4]))
        throw new Error(`${name}: it scrolls inside, so part of it is hidden; make the window taller`)
      const x = Math.ceil(Math.min(...boxes.map((b) => b[0])) * 2) / 2
      const y = Math.ceil(Math.min(...boxes.map((b) => b[1])) * 2) / 2
      const right = Math.floor(Math.max(...boxes.map((b) => b[2])) * 2) / 2
      const bottom = Math.floor(Math.max(...boxes.map((b) => b[3])) * 2) / 2
      const { width, height } = page.viewportSize()
      if (x < 0 || y < 0 || right > width || bottom > height)
        throw new Error(`${name}: the box runs out of the ${width}x${height} window; make it larger`)
      await page.screenshot({
        path: file,
        animations: 'disabled',
        clip: { x, y, width: right - x, height: bottom - y },
      })
      console.log('saved', file)
    }

    /**
     * Runs `fn(m, arg)` in the page, `m` being the mock's store (`/src/mocks/db.ts`: `db`,
     * `connectSigner`, `completeKeygen`, `logAccess`), the same module the app imported. It
     * stands in for what happens away from the console: an agent's signer, its reads.
     */
    const mock = (fn, arg) =>
      page.evaluate(
        async ([body, a]) => {
          const m = await import('/src/mocks/db.ts')
          return new Function('m', 'arg', `return (${body})(m, arg)`)(m, a)
        },
        [fn.toString(), arg],
      )

    /** A panel on a console page, by its title. */
    const card = (title) =>
      page
        .locator('[data-slot="card"]')
        .filter({ has: page.locator('[data-slot="card-title"]', { hasText: title }) })

    return { page, base, tidy, shot, mock, card, close }
  } catch (e) {
    await close()
    throw e
  }
}

async function answers(url) {
  try {
    return (await fetch(url)).ok
  } catch {
    return false
  }
}

/** A browser wallet holding one address, on Arbitrum Sepolia; it signs nothing. */
function fakeWallet(address) {
  window.ethereum = {
    request: async ({ method }) => {
      await new Promise((r) => setTimeout(r, 150))
      if (method === 'eth_requestAccounts' || method === 'eth_accounts') return [address]
      if (method === 'eth_chainId') return '0x66eee'
      throw new Error('unsupported ' + method)
    },
    on() {},
    removeListener() {},
  }
}

/**
 * Runs in the page before the app: rewrites what only the mock shows, every time the page
 * changes, so a re-render can't bring it back. The rules, added with `__shots.add`:
 * - `subs`: [pattern, flags, replacement] on every text node;
 * - `checks`: { [check name]: detail } for a venue check's detail line where the mock fills
 *   it with its paper-mode text;
 * - `hide`: [selector, text] hides each element matching the selector whose text includes it.
 * Toasts never show.
 */
function installTidy(initial) {
  // What the mock's checks say where a venue would answer: its paper mode, and the Binance
  // environment by its code.
  const MOCK_DETAILS = ['Paper mode: simulated', 'demo']
  const cfg = { subs: [], checks: {}, hide: [] }
  let compiled = []
  function apply() {
    if (!document.body) return
    const walk = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
    for (let n = walk.nextNode(); n; n = walk.nextNode()) {
      let t = n.textContent
      for (const [re, w] of compiled) t = t.replace(re, w)
      // A venue check is two lines: its name, then its detail.
      const name = n.parentElement?.previousElementSibling?.textContent
      if (
        name in cfg.checks &&
        MOCK_DETAILS.includes(t) &&
        n.parentElement.parentElement?.closest('li')
      )
        t = cfg.checks[name]
      if (t !== n.textContent) n.textContent = t
    }
    for (const [selector, text] of cfg.hide)
      for (const el of document.querySelectorAll(selector))
        if (el.textContent.includes(text) && el.style.display !== 'none') el.style.display = 'none'
  }
  window.__shots = {
    apply,
    add(more) {
      cfg.subs.push(...(more.subs ?? []))
      Object.assign(cfg.checks, more.checks ?? {})
      cfg.hide.push(...(more.hide ?? []))
      compiled = cfg.subs.map(([s, f, w]) => [new RegExp(s, f), w])
      apply()
    },
  }
  window.__shots.add(initial)
  document.addEventListener('DOMContentLoaded', () => {
    const style = document.createElement('style')
    style.textContent = '[data-sonner-toaster] { display: none !important }'
    document.head.append(style)
    new MutationObserver(apply).observe(document.body, {
      subtree: true,
      childList: true,
      characterData: true,
    })
    apply()
  })
}
