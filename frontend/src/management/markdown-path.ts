// A local Markdown target is relative to an explicitly chosen workspace.
// Never turn host paths, URL schemes or traversal into a device read.
export function localMarkdownPath(value: string | undefined): string | null {
  if (!value || value.trim() !== value) return null
  try {
    const decoded = decodeURIComponent(value)
    if (!decoded || decoded.length > 4096 || /^[a-z][a-z\d+.-]*:/i.test(decoded) || /^[/?#]/.test(decoded) || decoded.includes('\\') || [...decoded].some(char => char.charCodeAt(0) < 32 || char.charCodeAt(0) === 127)) return null
    const parts = decoded.split('/')
    if (parts.some(part => part === '..' || part === '')) return null
    const path = parts.filter(part => part !== '.').join('/')
    return path || null
  } catch { return null }
}

// Run after HTML sanitization but before harden resolves/blocks relative URLs.
// AST data cannot be supplied by raw HTML; the renderer uses only this frozen path.
type MarkdownNode = { tagName?: string; properties?: Record<string, unknown>; data?: Record<string, unknown>; children?: MarkdownNode[] }
export function workspacePathFromNode(node: { data?: object } | undefined): string | null {
  const data = node?.data
  return data && 'workspacePath' in data && typeof data.workspacePath === 'string' ? data.workspacePath : null
}
export function workspaceMarkdownLinks() {
  return (tree: MarkdownNode) => {
    const visit = (node: MarkdownNode) => {
      const key = node.tagName === 'a' ? 'href' : node.tagName === 'img' ? 'src' : null
      const raw = key && node.properties?.[key]
      if (key && typeof raw === 'string') {
        const path = localMarkdownPath(raw)
        if (path) {
          node.data = { ...node.data, workspacePath: path }
          node.properties![key] = 'https://juex-workspace.invalid/'
        } else if (!/^[a-z][a-z\d+.-]*:/i.test(raw) && !raw.startsWith('#') && !raw.startsWith('//')) {
          delete node.properties![key]
        }
      }
      node.children?.forEach(visit)
    }
    visit(tree)
  }
}
