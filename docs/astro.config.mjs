// @ts-check
import { defineConfig } from 'astro/config'
import starlight from '@astrojs/starlight'
import { analyticsHead } from './src/analytics'
import { satteri } from '@astrojs/markdown-satteri'
import { cliVersion, cliVersionPlugin } from './src/lib/cli-version.mjs'

// The CLI release the install page names: the latest on GitHub, or MISHMAR_VERSION.
const MISHMAR_VERSION = await cliVersion()

// The docs site, docs.mishmar.io: Starlight over the Markdown in src/content/docs.
// Every page is also served as Markdown (src/pages), for agents.
export default defineConfig({
  site: 'https://docs.mishmar.io',
  // Astro's default Markdown processor, with one plugin added; Starlight appends its own.
  markdown: { processor: satteri({ mdastPlugins: [cliVersionPlugin(MISHMAR_VERSION)] }) },
  vite: { define: { __MISHMAR_VERSION__: JSON.stringify(MISHMAR_VERSION) } },
  // Pages that moved keep their old address. Each one is in firebase.json's redirects too,
  // as a 301 for the page and for its .md copy; this map serves dev and preview, and is
  // the fallback page (a meta refresh) if Hosting's redirect is ever missing.
  redirects: {
    // Contract 0.20.0 (12 Nov 2026): trading accounts became connected accounts.
    '/hyperliquid/add-a-trading-account': '/hyperliquid/connect-an-account/',
    '/binance/add-a-trading-account': '/binance/connect-an-account/',
    // Contract 0.21.0 (13 Nov 2026): portfolios are gone; their page became where rules apply.
    '/start/portfolios': '/start/where-rules-apply/',
  },
  integrations: [
    starlight({
      title: 'Mishmar docs',
      description:
        'Risk management for trading bots and AI agents: your limits on every order, your approval on the big ones, and no key that can trade alone. The setup guides, step by step.',
      logo: {
        dark: './src/assets/brand/mark-dark.svg',
        light: './src/assets/brand/mark-light.svg',
      },
      favicon: '/favicon.svg',
      head: analyticsHead(),
      social: [
        { icon: 'github', label: 'GitHub', href: 'https://github.com/mishmar-io/mishmar' },
        { icon: 'x.com', label: 'X', href: 'https://x.com/mishmar_io' },
        { icon: 'discord', label: 'Discord', href: 'https://mishmar.io/discord' },
      ],
      editLink: { baseUrl: 'https://github.com/mishmar-io/mishmar/edit/main/docs/' },
      customCss: [
        '@fontsource-variable/geist',
        '@fontsource-variable/geist-mono',
        './src/styles/mishmar.css',
      ],
      routeMiddleware: './src/routeData.ts',
      sidebar: [
        { label: 'Start here', items: [{ autogenerate: { directory: 'start' } }] },
        { label: 'Hyperliquid', items: [{ autogenerate: { directory: 'hyperliquid' } }] },
        { label: 'Binance', items: [{ autogenerate: { directory: 'binance' } }] },
        { label: 'How it works', items: [{ autogenerate: { directory: 'concepts' } }] },
        { label: 'AI assistants', items: [{ autogenerate: { directory: 'assistants' } }] },
        { label: 'Questions', items: [{ autogenerate: { directory: 'questions' } }] },
      ],
    }),
  ],
})
