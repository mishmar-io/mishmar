# docs.mishmar.io

The docs site: step-by-step guides for people and for agents. Starlight (Astro) over the
Markdown in `src/content/docs`; every page is also served as Markdown at its own address
with `.md` on the end, and `/llms.txt` lists them all.

```
npm install
npm run dev       # http://localhost:4321, drafts included
npm run build     # dist/: the site, the .md copies, llms.txt, llms-full.txt, the search index
npm run preview   # serve dist/
```

## Layout

```
src/content/docs/   the pages; the folder is the sidebar section (start/, hyperliquid/, later binance/, concepts/)
public/screens/     screenshots, one folder per page: public/screens/<section>/<page>/<name>.png
src/lib/markdown.ts the sections in order, and a page as an agent reads it
src/pages/          [...slug].md.ts (each page as Markdown), llms.txt.ts, llms-full.txt.ts
src/routeData.ts    each page's <link rel="alternate" type="text/markdown">
src/styles/         the mishmar.io look on Starlight
src/middleware.ts   screenshots get their size (half the 2x PNG's) on every page
scripts/            one screenshot script per page, against the console's mock engine (shots-common.mjs: what they share)
```

A new section is a folder under `src/content/docs`, an entry in `astro.config.mjs`'s sidebar
and one in `SECTIONS` in `src/lib/markdown.ts`.

## Writing a page

Plain `.md`, never MDX: the source is what an agent reads, so it must make sense without
the layout. Front matter: `title`, a `description` that says what the page gets done (it is
the line in `llms.txt`), `sidebar.order`, and `lastUpdated` set to the day you last walked
through the steps against the product. `draft: true` keeps a page out of the build while
the product catches up.

A task page goes in this order: what you end up with, **What you need**, numbered steps
(one `##` per step, the button and field names in bold exactly as the screen spells them),
**Check it worked**, **When something goes wrong** (the exact error text, then the fix).

- Every command is complete and copyable, with `…` where the reader's own id goes. Show
  the output it prints, from the code, not from memory.
- Never put a step only in a screenshot. The text carries every step; the screenshot
  confirms it. Alt text says what the screen shows.
- Screenshots come from the console's mock engine (`mishmar-console`, dark theme, 2x),
  cropped to the dialog or panel, through a script in `scripts/` so they can be taken
  again when a screen changes: `node scripts/shots-hyperliquid-agent.mjs ../../mishmar-console
  public/screens/hyperliquid/add-an-agent`. The script swaps what only the mock shows (its
  engine address, short ids, paper-mode details) for what a real account shows, and masks
  secrets. Screenshots never show a real key, secret, address or account. Each script starts
  the console on its own port (5182 to 5187) and stops if something already answers there;
  look at every PNG it writes before you use it.
- Link other pages by their path with a trailing slash (`/start/install-the-cli/`) and
  images by their path under `public/` (`/screens/…png`). The `.md` copies turn both into
  absolute URLs, page links pointing at the other page's `.md`.

A page that moves keeps its old address working. Add the old path to `redirects` in
`astro.config.mjs`, and to `redirects` in `firebase.json` twice, for the page and for its `.md`
copy, as a 301. Hosting answers a redirect before any file, so the meta-refresh page Astro writes
at the old path only serves `npm run dev` and `preview`. The two account pages moved this way on
12 Nov 2026 (`add-a-trading-account` became `connect-an-account`), and `start/portfolios` became
`start/where-rules-apply` on 13 Nov 2026, when portfolios were removed.

## Visit counts

`src/analytics.ts` loads Umami Cloud's script on every page: cookieless and no personal data,
so no consent banner. It counts only on docs.mishmar.io, never under `npm run dev`, `preview` or
the web.app address, and an empty `UMAMI_WEBSITE_ID` turns it off. The free plan has one
website, so the docs count into mishmar.io's: filter by hostname `docs.mishmar.io` to see the
docs alone. Umami records page views with their referrer and query string, and one event:

| Event  | When                                  | `step`                                                                   |
| ------ | ------------------------------------- | ------------------------------------------------------------------------ |
| `copy` | a click on a code block's copy button | the id of the `##` above it, e.g. `5-make-the-key-on-the-agents-machine` |

The privacy policy (`mishmar-site/src/privacy.tsx`) lists these; change it with this table.
`.md` copies and `llms.txt` run no script, so agents reading them are not counted.

## Deploy

Firebase Hosting, site `mishmar-docs` (target `docs`) in the mishmar-511602 project, at
docs.mishmar.io:

```
npm run build && firebase deploy --only hosting:docs
```

A published release deploys the docs by itself: `.github/workflows/docs.yml` builds main and
deploys it, as `docs-deployer` through the prod project's GitHub pool
(`scripts/deploy-identity.sh` sets that up, once). It builds main, not the tag, so commit docs
changes before publishing a release, or the deploy replaces a hand deploy made from a working
tree. Started by hand (`gh workflow run docs.yml --ref main`), it redeploys main.

### The CLI version

Pages write `{{MISHMAR_VERSION}}` where the mishmar CLI version goes (the install page does).
The build fills it in with the latest published release on GitHub, in the site, the `.md`
copies and `llms-full.txt` (`src/lib/cli-version.mjs`), so the docs never pin an old release.
`MISHMAR_VERSION=v1.2.3` overrides the lookup; set it to build without network.
