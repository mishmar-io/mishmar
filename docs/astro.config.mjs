// @ts-check
import { defineConfig } from 'astro/config'
import starlight from '@astrojs/starlight'
import { analyticsHead } from './src/analytics'

// The docs site, docs.mishmar.io: Starlight over the Markdown in src/content/docs.
// Every page is also served as Markdown (src/pages), for agents.
export default defineConfig({
  site: 'https://docs.mishmar.io',
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
