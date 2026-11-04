import type { APIRoute } from 'astro'
import { pages, toMarkdown } from '../lib/markdown'

/** Every page in one file, for an agent that wants the whole set in its context. */
export const GET: APIRoute = async ({ site }) =>
  new Response((await pages()).map((p) => toMarkdown(p, site!)).join('\n---\n\n'), {
    headers: { 'Content-Type': 'text/plain; charset=utf-8' },
  })
