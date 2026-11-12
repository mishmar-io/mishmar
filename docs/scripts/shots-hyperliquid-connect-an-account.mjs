// Screenshots for the docs page "Connect a Hyperliquid account", from the console's mock engine.
// Usage: node scripts/shots-hyperliquid-connect-an-account.mjs ../../mishmar-console public/screens/hyperliquid/connect-an-account
import { PAGE_VIEWPORT, start } from './shots-common.mjs'

const PORT = 5182
// Made up, in the shape of a Hyperliquid master address.
const MASTER = '0x4f3a9b2c7d1e0f6a5b4c3d2e1f0a9b8c7d6e5f4a'
const ID = 'ta_2xvmaf3cmvxw'

const { page, base, tidy, shot, card, close } = await start({ port: PORT, wallet: MASTER })
try {
  await page.goto(`${base}/connected-accounts`)
  // What the mock shows differently from a real account: short ids, and the paper-mode check
  // details, which read as Hyperliquid's answers (the address, its margin).
  await tidy({
    subs: [['\\bta_[a-z0-9]{1,4}\\b', 'g', ID]],
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

  // Check it worked: the account's page, its Setup, Verification and Data, stacked.
  await dialog.getByRole('button', { name: 'Done' }).click()
  await page.waitForURL(/\/connected-accounts\/ta_/)
  await page.setViewportSize(PAGE_VIEWPORT)
  await card('Who trades here').waitFor()
  await page.waitForTimeout(800)
  await shot('account-page', [card(/^Setup$/), card(/^Verification/), card(/^Data/)])

  // Who trades here.
  await shot('who-trades-here', card('Who trades here'))
} finally {
  await close()
}
