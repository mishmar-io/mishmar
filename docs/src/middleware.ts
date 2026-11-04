import { readFileSync } from 'node:fs'
import path from 'node:path'
import { defineMiddleware } from 'astro:middleware'

/**
 * Screenshots under /screens/ are captured at 2x, so each <img> gets its PNG's width and
 * height halved: it shows at the size the screen had instead of stretched to the column,
 * and takes its space before it loads. Runs in dev and when the pages are prerendered.
 */
export const onRequest = defineMiddleware(async (_, next) => {
  const res = await next()
  if (!res.headers.get('content-type')?.startsWith('text/html')) return res
  const html = (await res.text()).replace(
    /<img src="(\/screens\/[^"]+\.png)"/g,
    (tag, src: string) => {
      const png = readFileSync(path.join(process.cwd(), 'public', src))
      const [width, height] = [png.readUInt32BE(16) / 2, png.readUInt32BE(20) / 2]
      return `${tag} width="${width}" height="${height}" loading="lazy"`
    },
  )
  return new Response(html, res)
})
