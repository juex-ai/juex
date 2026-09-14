import assert from 'node:assert/strict';
import test from 'node:test';
import { filterFileTree, findFiles, fileLanguage } from '../../frontend/src/lib/file-browser.ts';
import type { FileNode } from '../../frontend/src/types.ts';

const file = (path: string): FileNode => ({name:path.split('/').at(-1)!,path,is_dir:false});
const tree: FileNode = {name:'.workspace',path:'/',is_dir:true,children:[
  {name:'src',path:'src',is_dir:true,children:[file('src/App.tsx'),file('src/.env')]},
  {name:'.cache',path:'.cache',is_dir:true,children:[file('.cache/App.tsx')]},
  {name:'deep',path:'deep',is_dir:true,children_truncated:true}, file('README.md')
]};

test('hidden filtering preserves the root and truncation without mutating the source', () => {
  const visible = filterFileTree(tree, false);
  assert.deepEqual(visible.children?.map(n=>n.name), ['src','deep','README.md']);
  assert.deepEqual(visible.children?.[0].children?.map(n=>n.name), ['App.tsx']);
  assert.equal(visible.children?.[1].children_truncated,true);
  assert.equal(tree.children?.length,4);
  assert.equal(filterFileTree(tree,true),tree);
});
test('find files matches full paths case insensitively, including collapsed descendants', () => {
  assert.deepEqual(findFiles(filterFileTree(tree,false),' SRC/app ').files.map(n=>n.path),['src/App.tsx']);
  assert.deepEqual(findFiles(tree,'app.tsx').files.map(n=>n.path),['src/App.tsx','.cache/App.tsx']);
  assert.equal(findFiles(tree,'missing').truncated,true);
  assert.deepEqual(findFiles(tree,'missing').files,[]);
});
test('preview language follows filename and keeps unknown content as plain text', () => {
  assert.equal(fileLanguage('nested/INDEX.HTML'),'html');
  assert.equal(fileLanguage('cmd/main.go'),'go');
  assert.equal(fileLanguage('src/a.tsx'),'tsx');
  assert.equal(fileLanguage('README.md'),'markdown');
  assert.equal(fileLanguage('notes.unknown'),'text');
});
