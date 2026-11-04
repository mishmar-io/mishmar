import { getCollection, type CollectionEntry } from 'astro:content'

export type Page = CollectionEntry<'docs'>

/** The sections, by top-level folder, in the sidebar's order; the home page belongs to the first. */
export const SECTIONS: [folder: string, label: string][] = [
  ['start', 'Start here'],
  ['hyperliquid', 'Hyperliquid'],
  ['binance', 'Binance'],
  ['concepts', 'How it works'],
]

/** The page's section: its top-level folder, or `start` for the home page. */
export function sectionOf(page: Page): string {
  return page.id === 'index' ? 'start' : page.id.split('/')[0]
}

/** Where a page's Markdown is served: /index.md for the home page, /<id>.md for the others. */
export function markdownPath(id: string): string {
  return `/${id === '' ? 'index' : id}.md`
}

/** Every page in sidebar order: by section, then by `sidebar.order`; the home page first. */
export async function pages(): Promise<Page[]> {
  const all = await getCollection('docs', (p) => !p.data.draft)
  const rank = (p: Page) => {
    const i = SECTIONS.findIndex(([folder]) => folder === sectionOf(p))
    return i === -1 ? SECTIONS.length : i
  }
  const order = (p: Page) => (p.id === 'index' ? -Infinity : (p.data.sidebar.order ?? Infinity))
  return all.sort(
    (a, b) => rank(a) - rank(b) || order(a) - order(b) || a.data.title.localeCompare(b.data.title),
  )
}

/**
 * A page as an agent reads it: title, description, then the source with every site link made
 * absolute, links to other pages pointing at their Markdown, so it stands on its own.
 * Pages are plain Markdown (no MDX), so the source is what a person reads, less the layout.
 */
export function toMarkdown(page: Page, site: URL): string {
  const body = (page.body ?? '').replace(
    /\]\(\/([^)\s#]*)(#[^)\s]*)?\)/g,
    (_, path: string, hash = '') => {
      // A page link ends in a slash; anything with an extension is a file (a screenshot).
      const target =
        path === '' || path.endsWith('/') ? markdownPath(path.replace(/\/$/, '')) : `/${path}`
      return `](${new URL(target, site).href}${hash})`
    },
  )
  return `# ${page.data.title}\n\n> ${page.data.description ?? ''}\n\n${body.trim()}\n`
}
