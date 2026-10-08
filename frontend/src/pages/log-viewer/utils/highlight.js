// ============================================
// 日志搜索高亮：把文本按关键词（不区分大小写）切成片段
// 2026-10-07：关键词按字面量匹配——旧实现直接 new RegExp(用户输入)，输入 ( [ 等会抛异常导致整页空白
// ============================================

const escapeRegExp = (text) => text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

// 返回 [{ text, match }]；没有关键词时整段原样返回
export const splitByKeyword = (text, keyword) => {
  const source = String(text ?? '');
  if (!keyword) return [{ text: source, match: false }];
  const pattern = new RegExp(`(${escapeRegExp(keyword)})`, 'gi');
  return source
    .split(pattern)
    .filter((part) => part !== '')
    .map((part) => ({ text: part, match: part.toLowerCase() === keyword.toLowerCase() }));
};
