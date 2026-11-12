// The mishmar CLI release the docs tell people to install: the latest published release on
// GitHub, read once per build, so a build after a release names it with no edit. A page writes
// {{MISHMAR_VERSION}} where the version goes; cliVersionPlugin fills it in on the site, and
// toMarkdown (src/lib/markdown.ts) in the .md copies and llms-full.txt.
//
// MISHMAR_VERSION in the environment skips the lookup: the docs workflow passes the tag of the
// release that started it, and a build without network needs it.

export const PLACEHOLDER = '{{MISHMAR_VERSION}}'

const LATEST = 'https://api.github.com/repos/mishmar-io/mishmar/releases/latest'

/** @returns {Promise<string>} the version, such as v0.3.0 */
export async function cliVersion(env = process.env) {
  const version = env.MISHMAR_VERSION || (await latestRelease(env.GITHUB_TOKEN))
  if (!/^v\d+\.\d+\.\d+$/.test(version)) {
    throw new Error(`mishmar CLI version: want v<major>.<minor>.<patch>, got ${JSON.stringify(version)}`)
  }
  return version
}

/** GitHub's latest release: the newest published one, never a draft or a pre-release. */
async function latestRelease(token) {
  const headers = { accept: 'application/vnd.github+json', 'user-agent': 'mishmar-docs-build' }
  if (token) headers.authorization = `Bearer ${token}`
  const offline = 'set MISHMAR_VERSION=vX.Y.Z to build without asking GitHub'
  let res
  try {
    res = await fetch(LATEST, { headers })
  } catch (err) {
    throw new Error(`mishmar CLI version: could not reach GitHub (${err.message}); ${offline}`)
  }
  if (!res.ok) throw new Error(`mishmar CLI version: GitHub answered ${res.status} for the latest release; ${offline}`)
  return (await res.json()).tag_name
}

/**
 * A plugin for Astro's Markdown processor (Sätteri) that puts the version wherever a page wrote
 * the placeholder, in prose, inline code and code blocks.
 */
export function cliVersionPlugin(version) {
  const fill = (node, ctx) => {
    if (node.value.includes(PLACEHOLDER)) {
      ctx.replaceNode(node, { ...node, value: node.value.replaceAll(PLACEHOLDER, version) })
    }
  }
  return { name: 'mishmar-cli-version', text: fill, inlineCode: fill, code: fill }
}
