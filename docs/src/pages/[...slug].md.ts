import type { APIRoute, GetStaticPaths } from 'astro'
import { markdownPath, pages, toMarkdown, type Page } from '../lib/markdown'

/** Every page again as Markdown at /<page>.md, for agents and for pasting into one. */
export const getStaticPaths = (async () =>
  (await pages()).map((page) => ({
    params: { slug: markdownPath(page.id).slice(1, -'.md'.length) },
    props: { page },
  }))) satisfies GetStaticPaths

export const GET: APIRoute<{ page: Page }> = ({ props, site }) =>
  new Response(toMarkdown(props.page, site!), {
    headers: { 'Content-Type': 'text/markdown; charset=utf-8' },
  })
