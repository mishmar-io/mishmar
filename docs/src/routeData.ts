import { defineRouteMiddleware } from '@astrojs/starlight/route-data'
import { markdownPath } from './lib/markdown'

/** Each page points at its Markdown twin, so an agent that lands on the HTML finds the source. */
export const onRequest = defineRouteMiddleware((context) => {
  const route = context.locals.starlightRoute
  route.head.push({
    tag: 'link',
    attrs: { rel: 'alternate', type: 'text/markdown', href: markdownPath(route.id) },
  })
})
