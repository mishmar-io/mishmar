// @ts-check
import { defineConfig } from 'astro/config'
import starlight from '@astrojs/starlight'

// The docs site, docs.mishmar.io: Starlight over the Markdown in src/content/docs.
// Every page is also served as Markdown (src/pages), for agents.
export default defineConfig({
  site: 'https://docs.mishmar.io',
  integrations: [
    starlight({
      title: 'Mishmar docs',
      description:
        'How to set up Mishmar: trading accounts, agents and their split keys on Hyperliquid and Binance, step by step.',
      logo: {
        dark: './src/assets/brand/mark-dark.svg',
        light: './src/assets/brand/mark-light.svg',
      },
      favicon: '/favicon.svg',
      social: [
        { icon: 'github', label: 'GitHub', href: 'https://github.com/mishmar-io/mishmar' },
        { icon: 'x.com', label: 'X', href: 'https://x.com/mishmar_io' },
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
      ],
    }),
  ],
})
