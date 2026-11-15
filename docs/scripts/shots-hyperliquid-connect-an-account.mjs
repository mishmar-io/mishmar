// Screenshots for the docs page "Connect a Hyperliquid account", from the console's mock engine.
// Usage: node scripts/shots-hyperliquid-connect-an-account.mjs ../../mishmar-console public/screens/hyperliquid/connect-an-account
import { PAGE_VIEWPORT, start } from './shots-common.mjs'

const PORT = 5182
// Made up, in the shape of a Hyperliquid master address; not the fixture's, which is connected
// already.
const MASTER = '0x8b1d4e7a2c9f3b6d0e5a8c1f4b7d2e9a6c3f0b5d'
const ID = 'ta_2xvmaf3cmvxw'
const CONNECTION = 'vc_6hq2mz8rkt4w'

const { page, base, tidy, shot, card, close } = await start({ port: PORT, wallet: MASTER })
try {
  await page.goto(`${base}/connected-accounts`)
  // What the mock shows differently from a real account: short ids, and the paper-mode check
  // details, which read as Hyperliquid's answers (the address, its margin).
  await tidy({
    subs: [
      ['\\bta_[a-z0-9]{1,4}\\b', 'g', ID],
      ['\\bvc_[a-z0-9]{1,4}\\b', 'g', CONNECTION],
    ],
    checks: {
      'Master account found on the network': MASTER,
      'Master account has margin': '$1,249.62 available',
    },
  })
  const dialog = page.getByRole('dialog')

  // 1. Connected accounts → Connect account: the venue.
  await page.getByRole('button', { name: 'Connect account' }).first().click()
  await dialog.getByText('Pick the venue.', { exact: false }).waitFor()
  await shot('pick-venue')

  // 2. The address, from the browser wallet; the network is the engine's.
  await dialog.getByRole('button', { name: 'Hyperliquid' }).click()
  await dialog.getByRole('button', { name: "Use my wallet's address" }).click()
  await page.waitForFunction((m) => document.getElementById('connect-address')?.value === m, MASTER)
  await dialog.getByText('Testnet').waitFor()
  await shot('address')

  // 3 and 4. Connect: Mishmar checks it (the dialog moves on once every check passes), then
  // reads it for the first time.
  await dialog.getByRole('button', { name: 'Connect', exact: true }).click()
  await dialog.getByRole('heading', { name: /is connected$/ }).waitFor({ timeout: 20000 })
  await shot('first-read')

  // Check it worked: Done opens the connection's page (a login with two accounts); its Setup
  // tab: Setup, Verification and Data, stacked.
  await dialog.getByRole('button', { name: 'Done' }).click()
  await page.waitForURL(/\/connections\/vc_/)
  await page.setViewportSize(PAGE_VIEWPORT)
  await page.getByRole('tab', { name: 'Setup' }).click()
  await card(/^Data/).getByText('Up to date').first().waitFor()
  await page.waitForTimeout(800)
  await shot('account-page', [card(/^Setup$/), card(/^Verification/), card(/^Data/)])

  // Who trades here, on the Perps account's page.
  await page.getByRole('tab', { name: 'Overview' }).click()
  await page.getByRole('link', { name: 'Perps', exact: true }).click()
  await page.waitForURL(/\/connected-accounts\/ta_/)
  await card('Who trades here').waitFor()
  await page.waitForTimeout(800)
  await shot('who-trades-here', card('Who trades here'))
} finally {
  await close()
}
