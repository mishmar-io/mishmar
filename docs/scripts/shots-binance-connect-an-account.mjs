// Screenshots for the docs page "Connect a Binance account", from the console's mock engine.
// Usage: node scripts/shots-binance-connect-an-account.mjs ../../mishmar-console public/screens/binance/connect-an-account
import { PAGE_VIEWPORT, start } from './shots-common.mjs'

const PORT = 5183
// Made up, in the shape Binance issues: 64 letters and digits. The secret field shows dots.
const READ_KEY = 'q7Xk2Rm9VbT4wLp8Nz3HcY6sJd1Fg5Ue0Ka7Qo2Wi9Mx4Bt8Ev3Ry6Cn1Pl5Sh0Z'
const READ_SECRET = 'not-a-real-secret-0000000000000000000000000000000000000000000000'
const ID = 'ta_7d2kq9wmx4pb'

const { page, base, tidy, shot, card, close } = await start({ port: PORT })
try {
  await page.goto(`${base}/connected-accounts`)
  // What the mock shows differently from a real account: short ids, and the paper-mode check
  // details, which read as Binance demo trading's answers, in the engine's words.
  await tidy({
    subs: [['\\bta_[a-z0-9]{1,4}\\b', 'g', ID]],
    checks: {
      'Read-only key accepted by venue': 'Accepted by Binance',
      'Read-only key cannot trade or withdraw':
        'Not reported by Binance demo trading; a live key must be able to read and nothing else',
      'Environment matches': 'Binance demo trading',
    },
  })
  const dialog = page.getByRole('dialog')

  // 2. Connected accounts → Connect account → Binance: the read-only key.
  await page.getByRole('button', { name: 'Connect account' }).first().click()
  await dialog.getByRole('button', { name: 'Binance' }).click()
  await page.getByLabel('Read-only API key').fill(READ_KEY)
  await page.getByLabel('Its secret key').fill(READ_SECRET)
  // The start of the key, not its end, in the field.
  await page.getByLabel('Read-only API key').evaluate((el) => {
    el.setSelectionRange(0, 0)
    el.scrollLeft = 0
  })
  await shot('read-only-key')

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
} finally {
  await close()
}
