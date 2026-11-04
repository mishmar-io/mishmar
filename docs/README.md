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
scripts/            one screenshot script per page, against the console's mock engine
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
  secrets. Screenshots never show a real key, secret, address or account.
- Link other pages by their path with a trailing slash (`/start/install-the-cli/`) and
  images by their path under `public/` (`/screens/…png`). The `.md` copies turn both into
  absolute URLs, page links pointing at the other page's `.md`.

## Deploy

Firebase Hosting, site `mishmar-docs` (target `docs`) in the mishmar-511602 project, at
docs.mishmar.io:

```
npm run build && firebase deploy --only hosting:docs
```
