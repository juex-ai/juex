import type { FileNode } from '../types';

export function filterFileTree(node: FileNode, showHidden: boolean): FileNode {
  if (showHidden) return node;
  return {
    ...node,
    children: node.children?.filter(child => !child.name.startsWith('.'))
      .map(child => filterFileTree(child, false)),
  };
}

export function findFiles(tree: FileNode, query: string): { files: FileNode[]; truncated: boolean } {
  const needle = query.trim().toLowerCase();
  const files: FileNode[] = [];
  let truncated = false;
  function visit(node: FileNode) {
    if (node.children_truncated) truncated = true;
    if (!node.is_dir && node.path.toLowerCase().includes(needle)) files.push(node);
    node.children?.forEach(visit);
  }
  visit(tree);
  return { files, truncated };
}

const languages: Record<string, string> = {
  js: 'javascript', mjs: 'javascript', cjs: 'javascript', jsx: 'jsx', ts: 'typescript', tsx: 'tsx',
  json: 'json', jsonl: 'json', html: 'html', htm: 'html', css: 'css', py: 'python',
  go: 'go', md: 'markdown', markdown: 'markdown', yaml: 'yaml', yml: 'yaml', sh: 'shellscript', bash: 'shellscript',
};
export function fileLanguage(path: string): string {
  return languages[path.split('.').at(-1)?.toLowerCase() ?? ''] ?? 'text';
}
