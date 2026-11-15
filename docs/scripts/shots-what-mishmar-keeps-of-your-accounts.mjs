// Screenshots for the docs page "What Mishmar keeps of your accounts", from the console's mock
// engine: a connected account's data tabs, its Data panel and who read it.
// Usage: node scripts/shots-what-mishmar-keeps-of-your-accounts.mjs ../../mishmar-console public/screens/concepts/what-mishmar-keeps-of-your-accounts
import { PAGE_VIEWPORT, start } from './shots-common.mjs'

const PORT = 5187
// Made up, in the shape of a Hyperliquid master address; not the fixture's, which is connected
// already.
const MASTER = '0x8b1d4e7a2c9f3b6d0e5a8c1f4b7d2e9a6c3f0b5d'
// The fixture's Hyperliquid Perps account, which two of its agents trade on, and its connection.
const ACCOUNT = 'rt_hl_main'
const CONNECTION = 'vc_hl_main'

const { page, base, tidy, shot, mock, card, close } = await start({
  port: PORT,
  viewport: PAGE_VIEWPORT,
  wallet: MASTER,
})
try {
  const tab = async (name, title) => {
    await page.goto(`${base}/connected-accounts/${ACCOUNT}?tab=${name}`)
    await card(title).locator('tbody tr').first().waitFor()
    await page.waitForTimeout(600)
  }
  /** A long table cut to its newest rows, without the count under it. */
  const firstRows = (n) =>
    page.addStyleTag({
      content: `[data-slot=card] tbody tr:nth-child(n+${n + 1}) { display: none }`,
    })
  await page.goto(`${base}/connected-accounts`)
  await tidy({
    subs: [
      [`\\b${ACCOUNT}\\b`, 'g', 'ta_2xvmaf3cmvxw'],
      ['\\bta_[a-z0-9]{1,4}\\b', 'g', 'ta_5mkq8xr2vd7c'],
      [`\\b${CONNECTION}\\b`, 'g', 'vc_9tf4xk2mwq7h'],
      ['\\bvc_[a-z0-9]{1,4}\\b', 'g', 'vc_6hq2mz8rkt4w'],
    ],
    hide: [['[data-slot=card-content] > div', ' loaded']],
  })

  // What it keeps: balances, the venue's positions, the value over time, the transactions.
  await tab('balances', /^Balances/)
  await shot('balances', card(/^Balances/))
  await tab('positions', /^At the venue/)
  await shot('positions', card(/^At the venue/))
  await page.goto(`${base}/connected-accounts/${ACCOUNT}?tab=value`)
  await card(/^Value/).getByText(/one point per hour/).waitFor()
  await page.waitForTimeout(600)
  await shot('value', card(/^Value/))
  await tab('transactions', /^Transactions/)
  await firstRows(10)
  await shot('transactions', card(/^Transactions/))

  // Who read it: the account's Access tab.
  await tab('access', /^Who read it/)
  await firstRows(8)
  await shot('access', card(/^Who read it/))

  // How current it is: the Data panel on the connection's Setup tab.
  await page.goto(`${base}/connections/${CONNECTION}?tab=setup`)
  await card(/^Data/).getByText('Up to date').first().waitFor()
  await page.waitForTimeout(600)
  await shot('data', card(/^Data/))

  // An account connected a moment ago, still reading its history. The first read is held
  // partway, so the shot doesn't race the page's polling.
  await page.getByRole('link', { name: 'Connected accounts' }).first().click()
  await page.getByRole('button', { name: 'Connect account' }).first().click()
  const dialog = page.getByRole('dialog')
  await dialog.getByRole('button', { name: 'Hyperliquid' }).click()
  await dialog.getByRole('button', { name: "Use my wallet's address" }).click()
  await page.waitForFunction((m) => document.getElementById('connect-address')?.value === m, MASTER)
  await dialog.getByRole('button', { name: 'Connect', exact: true }).click()
  await dialog.getByRole('heading', { name: /^Reading / }).waitFor()
  await mock((m) => {
    const id = m.db.venueConnections.at(-1).id
    setInterval(() => m.db.syncStarted.set(id, Date.now() - 3600), 100)
  })
  await dialog.getByRole('button', { name: 'Done' }).click()
  await page.waitForURL(/\/connections\/vc_/)
  await page.getByRole('tab', { name: 'Setup' }).click()
  await card(/^Data/).getByText(/Reading its history · \d+%/).waitFor({ timeout: 15000 })
  await page.waitForTimeout(600)
  await shot('data-reading-history', card(/^Data/))
} finally {
  await close()
}
