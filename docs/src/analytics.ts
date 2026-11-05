import type { StarlightUserConfig } from '@astrojs/starlight/types'

/**
 * Visit counts through Umami Cloud, as on mishmar.io. It sets no cookies and keeps no personal
 * data, so the docs need no consent banner. Nothing loads while the website id is empty, and the
 * script counts only on docs.mishmar.io, so `npm run dev`, previews and the web.app address stay
 * out of the numbers.
 */
export const UMAMI_SCRIPT = 'https://cloud.umami.is/script.js'
export const COUNTED_DOMAIN = 'docs.mishmar.io'

/**
 * The Umami Cloud website the docs count into. Empty counts nothing. The free plan has one
 * website, so it is mishmar.io's, shared: filter by hostname to see the docs alone.
 */
export const UMAMI_WEBSITE_ID = 'f1bedb1d-748b-4459-8f3b-89d9041f05c9'

/**
 * The one event besides page views: `copy`, a click on a code block's copy button, with `step`,
 * the id of the `##` above it, so a guide shows how far its readers get. Umami counts a click on
 * any button that carries these attributes.
 */
const MARK_COPY_BUTTONS = `addEventListener('DOMContentLoaded', () => {
  for (const b of document.querySelectorAll('.expressive-code .copy button')) {
    b.dataset.umamiEvent = 'copy'
    let n = b.closest('.sl-markdown-content > *')
    while (n && !n.matches('.sl-heading-wrapper.level-h2')) n = n.previousElementSibling
    const step = n?.querySelector('h2')?.id
    if (step) b.dataset.umamiEventStep = step
  }
})`

export function analyticsHead(websiteId = UMAMI_WEBSITE_ID): StarlightUserConfig['head'] {
  if (!websiteId) return []
  return [
    {
      tag: 'script',
      attrs: {
        defer: true,
        src: UMAMI_SCRIPT,
        'data-website-id': websiteId,
        'data-domains': COUNTED_DOMAIN,
      },
    },
    { tag: 'script', content: MARK_COPY_BUTTONS },
  ]
}
