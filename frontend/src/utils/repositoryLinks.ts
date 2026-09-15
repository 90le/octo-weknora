// Repository links resolve against the cited commit, never the dashboard URL.
export function resolveRepositoryLink(href: string, source: string): string | null {
  const match = /^https:\/\/github\.com\/([^/]+)\/([^/]+)\/blob\/([a-f0-9]{40})\//.exec(source)
  if (!match || !href || /^(?:[a-z][a-z0-9+.-]*:|\/\/|#)/i.test(href)) return href
  const root = `https://github.com/${match[1]}/${match[2]}/blob/${match[3]}/`
  try {
    const resolved = new URL(href.startsWith('/') ? href.slice(1) : href, href.startsWith('/') ? root : source)
    return resolved.href.startsWith(root) ? resolved.href : null
  } catch { return null }
}

export function resolveRepositoryHTMLLinks(html: string, source?: string): string {
  if (!source || !/^https:\/\/github\.com\/[^/]+\/[^/]+\/blob\/[a-f0-9]{40}\//.test(source)) return html
  const doc = new DOMParser().parseFromString(html, 'text/html')
  for (const anchor of doc.querySelectorAll('a[href]')) {
    const href = resolveRepositoryLink(anchor.getAttribute('href') || '', source)
    if (href === null) anchor.removeAttribute('href')
    else if (href !== anchor.getAttribute('href')) {
      anchor.setAttribute('href', href)
      anchor.setAttribute('target', '_blank')
      anchor.setAttribute('rel', 'noopener noreferrer')
    }
  }
  return doc.body.innerHTML
}
