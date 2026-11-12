import type { APIRoute } from 'astro'
import { SECTIONS, markdownPath, pages, sectionOf } from '../lib/markdown'

/** The index an agent reads first (llmstxt.org): what Mishmar is, then every page as Markdown. */
export const GET: APIRoute = async ({ site }) => {
  const all = await pages()
  const groups = [...SECTIONS, ['', 'More'] as const]
    .map(([folder, label]) => {
      const lines = all
        .filter((p) => (folder ? sectionOf(p) === folder : !SECTIONS.some(([f]) => f === sectionOf(p))))
        .map(
          (p) => `- [${p.data.title}](${new URL(markdownPath(p.id), site).href}): ${p.data.description ?? ''}`,
        )
      return lines.length ? `## ${label}\n\n${lines.join('\n')}` : ''
    })
    .filter(Boolean)
  const text = [
    '# Mishmar docs',
    '> Mishmar is a risk management layer for trading bots and AI agents: every order is checked against your limits before it reaches the exchange, large orders wait for your approval, and no key can trade alone.',
    'How to set up Mishmar, step by step: connected accounts, the agents that read them or trade on them, and the split keys they trade with. Each page below is plain Markdown. All of them in one file: ' +
      new URL('/llms-full.txt', site).href,
    ...groups,
    '## Optional\n\n- [Website](https://mishmar.io/llms.txt): what Mishmar is, who it is for\n- [Source](https://github.com/mishmar-io/mishmar): the CLI, the signer, the Python and TypeScript SDKs, the API contract',
  ]
  return new Response(text.join('\n\n') + '\n', {
    headers: { 'Content-Type': 'text/plain; charset=utf-8' },
  })
}
