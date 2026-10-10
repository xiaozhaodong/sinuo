import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

import { buildLifecycleSegments, railSegmentColors, lifecycleSegmentColors } from '../utils/lifecycle.js';
import { TABLE_COLUMNS, DEFAULT_VISIBLE_COLUMNS, UNSETTLED_STATUSES } from '../utils/constants.js';
import { STATUS_CONFIG } from '../../../utils/constants.js';

const HERE = dirname(fileURLToPath(import.meta.url));
const readSource = (relativePath) => readFileSync(join(HERE, relativePath), 'utf8');

const RAIL_SOURCE = readSource('./TraceRail.jsx');
const TABLE_SOURCE = readSource('./RequestsTable.jsx');
const CSS_SOURCE = readSource('../../../index.css');

// 断言「代码里有没有某个属性」时必须先去掉注释，否则解释「为什么不用 role="img"」
// 的那句注释自己就会把 doesNotMatch 判失败。
const stripComments = (source) => source
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/^[ \t]*\/\/.*$/gm, '');

const RAIL_CODE = stripComments(RAIL_SOURCE);
const TABLE_CODE = stripComments(TABLE_SOURCE);

// 轨道视觉档位。timeout / error 归入 failed —— 都是「没跑完」，
// 差异交给状态文字与详情面板。
const VARIANTS = ['pending', 'forwarding', 'processing', 'retry', 'suspended', 'completed', 'failed', 'cancelled'];
const IN_FLIGHT = ['pending', 'forwarding', 'processing', 'retry'];

const parseMap = (source, constName) => {
  const body = source.match(new RegExp(`const ${constName} = \\{([\\s\\S]*?)\\n\\};`))?.[1];
  assert.ok(body, `${constName} 未找到，测试与实现已脱节`);
  return body;
};

test('RAIL_VARIANT 覆盖 STATUS_CONFIG 的全部状态', () => {
  const body = parseMap(RAIL_SOURCE, 'RAIL_VARIANT');
  for (const status of Object.keys(STATUS_CONFIG)) {
    assert.match(body, new RegExp(`\\b${status}:`),
      `状态 ${status} 没有轨道档位，会掉进 || 'pending' 的兜底而显示成「在跑」`);
  }
});

test('终态都有末端符号，进行中都没有', () => {
  const body = parseMap(RAIL_SOURCE, 'CAP_ICON');
  for (const variant of VARIANTS) {
    const hasCap = new RegExp(`\\b${variant}:`).test(body);
    if (IN_FLIGHT.includes(variant)) {
      assert.equal(hasCap, false, `${variant} 是进行中，轨道右端必须空着表示「还没到终点」`);
    } else {
      assert.equal(hasCap, true, `${variant} 是终态，必须有不依赖颜色的末端符号`);
    }
  }
});

test('railSegmentColors 覆盖 buildLifecycleSegments 可能产出的全部 key', () => {
  // 三种降级路径 + 两种终态，覆盖 lifecycle.js 注释里列的 5 条规则
  const samples = [
    { duration: 1200 },                                                             // 规则 1：全无 timing
    { duration: 1200, firstTokenMs: 300, completionMs: 800 },                       // 规则 2：旧数据
    { duration: 1200, upstreamWriteMs: 200, firstTokenMs: 300, completionMs: 600 }, // 规则 3：无 routeDecisionAt
    {
      duration: 1200,
      timestamp: '2026-08-30T00:00:00.000000Z',
      routeDecisionAt: '2026-08-30T00:00:00.050000Z',
      upstreamWriteMs: 200,
      firstTokenMs: 300,
      completionMs: 400
    },                                                                              // 规则 4：全量
    { duration: 1200, firstTokenMs: 300, status: 'failed', failureReason: 'rate_limit: x' },
    { duration: 1200, firstTokenMs: 300, status: 'cancelled', cancelReason: 'client disconnect' }
  ];

  const seen = new Set();
  for (const sample of samples) {
    for (const segment of buildLifecycleSegments(sample)) seen.add(segment.key);
  }
  assert.ok(seen.size >= 5, `样本只覆盖到 ${seen.size} 种分段，不足以验证配色表`);

  for (const key of seen) {
    assert.ok(railSegmentColors[key], `分段 ${key} 缺轨道配色，会掉进 total 兜底而丢失语义`);
  }
});

test('两套分段配色的 key 完全一致', () => {
  // 轨道版与详情条版必须同构：同一个 buildLifecycleSegments 的输出
  // 要能同时喂给两边，任何一边缺 key 都会静默降级成灰色。
  assert.deepEqual(
    Object.keys(railSegmentColors).sort(),
    Object.keys(lifecycleSegmentColors).sort()
  );
});

test('轨道版配色不使用深色阶 —— 3px 细线上会读成「没渲染出来」', () => {
  for (const [key, value] of Object.entries(railSegmentColors)) {
    assert.doesNotMatch(value, /bg-\w+-(700|800|900)/,
      `${key} 的轨道色 ${value} 太深；需要压深的是详情条那套（${lifecycleSegmentColors[key]}），它要承载浅色标签文字`);
  }
  // 反向确认分叉理由成立：详情条那套确实存在深色阶，两者不是无谓的重复。
  const deepInDetail = Object.values(lifecycleSegmentColors).filter((value) => /bg-\w+-(700|800)/.test(value));
  assert.ok(deepInDetail.length > 0, '详情条配色里没有深色阶，两套配色的分叉理由已不成立');
});

test('进行中与已挂起都不产出分段 —— 不伪造进度', () => {
  // 这条锁的是 TraceRail 的核心契约：请求没结束前谁也不知道总时长。
  // suspended 尤其容易漏 —— 它不在 IN_FLIGHT 里（不该有流光），
  // 但请求同样没结束，照 duration 画分段等于伪造一条不存在的时间线。
  assert.match(RAIL_SOURCE, /const settled = !inFlight && variant !== 'suspended'/,
    'settled 的判据必须同时排除进行中与已挂起');
  assert.match(RAIL_SOURCE, /settled \? buildLifecycleSegments\(request\) : null/,
    '任何按 duration 画比例的写法都是编数据');

  // suspended 的半程条只能是静态装饰（flow），不能走 fill —— fill 承载的是
  // 按真实耗时算出来的分段比例，而那份数据此刻还不存在。
  assert.doesNotMatch(CSS_SOURCE, /trace-rail--suspended \.trace-rail__fill/,
    'suspended 不该给 fill 上样式，它没有可信的分段数据');
});

test('落位动画只在「亲眼看着它跑完」时播', () => {
  // 首屏与翻页拿到的终态行是历史数据，全表一起闪一遍毫无意义。
  assert.match(RAIL_SOURCE, /IN_FLIGHT_STATUSES\.has\(prev\)/,
    '判据必须包含「上一帧还在跑」，只看当前是不是终态会让首屏全表齐闪');
  assert.match(RAIL_SOURCE, /variant === 'cancelled'/,
    '取消是用户自己发起的，不需要终态反馈');
});

test('renderCell 每个分支都返回单一根元素', () => {
  // 入场位移挂在 `> td > *` 上（<tr> 横移会被 overflow-x 裁掉首列，
  // <td> 是 table-cell 吃不下 transform）。返回 Fragment 会让该列静默失去动画。
  const body = TABLE_SOURCE.match(/const renderCell = [\s\S]*?\n};/)?.[0];
  assert.ok(body, 'renderCell 未找到，测试与实现已脱节');

  assert.doesNotMatch(body, /return\s*\(\s*<>/, 'renderCell 不能返回 Fragment');
  assert.doesNotMatch(body, /return\s*<>/, 'renderCell 不能返回 Fragment');
  assert.doesNotMatch(body, /return\s*\[/, 'renderCell 不能返回数组');
});

test('单元格根元素不能是裸行内盒 —— transform 对它无效', () => {
  // 入场位移动的是 transform，而 transform 对非替换的行内元素完全无效。
  // 写成裸 <span> 那一列就只会淡入、不会跟着上浮，且失效是静默的。
  const body = TABLE_CODE.match(/const renderCell = [\s\S]*?\n};/)?.[0];
  assert.ok(body, 'renderCell 未找到，测试与实现已脱节');
  const valueCell = TABLE_CODE.match(/const ValueCell = [\s\S]*?\n\);/)?.[0];
  assert.ok(valueCell, 'ValueCell 未找到，测试与实现已脱节');

  // 只认根元素（return 后紧跟的那个），嵌套的 span 不受这条约束
  const roots = [
    ...body.matchAll(/return <span className=(?:\{`|")([^`"]*)/g),
    ...valueCell.matchAll(/<span className=(?:\{`|")([^`"]*)/g)
  ].map(match => match[1]);
  assert.ok(roots.length >= 4, `只扫到 ${roots.length} 个根 span，正则与实现已脱节`);

  for (const cls of roots) {
    assert.match(cls, /\b(?:inline-block|inline-flex|block|flex)\b/,
      `「${cls}」是裸行内盒，入场只会淡入不会上浮`);
  }
});

test('在途时 token 与成本显示占位符而不是 0', () => {
  // 后端只在终态写这几个字段，此刻的 0 是「还不知道」而不是「用了 0 个」。
  // 而且 0 是个合法数字 —— 从 0 换成别的数字眼睛不会注意到，
  // 从 — 变成数字才是一次「到货」，那才是完成瞬间最强的信号。
  assert.match(TABLE_CODE, /const PENDING_VALUE = '—'/, '占位符必须是破折号，不能是 0');
  assert.match(TABLE_CODE, /pending \? PENDING_VALUE : children/);

  // 已落定但用量未知（usage_known / cost_known 为 false）同理：没收到 usage 的
  // 失败/取消记录，0 是「还不知道」而不是「用了 0 个」。
  assert.match(TABLE_CODE, /const usagePending = pending \|\| request\.usageKnown === false/,
    'usage_known=false 的记录必须显示占位符');
  assert.match(TABLE_CODE, /const costPending = pending \|\| request\.costKnown === false/,
    'cost_known=false 的记录必须显示占位符');

  for (const col of ['inputTokens', 'outputTokens', 'cacheCreationTokens', 'cacheReadTokens', 'cost']) {
    assert.match(TABLE_CODE, new RegExp(`case '${col}':[\\s\\S]{0,160}?<ValueCell`),
      `${col} 没走 ValueCell，在途时会显示 0`);
  }

  // 判据是「还没落定」而不是「在跑」：suspended 不该有流光和跑秒，
  // 但它的 token 同样还没被写入。
  assert.equal(UNSETTLED_STATUSES.has('suspended'), true,
    'suspended 的请求没结束，token 还是 0，显示 0 等于陈述假事实');
  for (const status of IN_FLIGHT) {
    assert.equal(UNSETTLED_STATUSES.has(status), true, `${status} 必须显示占位符`);
  }
  for (const status of ['completed', 'failed', 'cancelled', 'timeout']) {
    assert.equal(UNSETTLED_STATUSES.has(status), false, `${status} 已落定，必须显示真实数字`);
  }
});

test('入场 class 的摘除认 animationName，且与 CSS 里的名字对得上', () => {
  const finalName = TABLE_SOURCE.match(/const FINAL_ENTER_ANIMATION = '([^']+)'/)?.[1];
  assert.ok(finalName, 'FINAL_ENTER_ANIMATION 未找到');

  // 必须是 CSS 里真实存在的 @keyframes，否则 animationend 永不匹配，
  // class 只能靠 setTimeout 兜底摘掉。
  assert.match(CSS_SOURCE, new RegExp(`@keyframes ${finalName}\\b`),
    `${finalName} 在 index.css 里没有对应的 @keyframes`);

  // 且必须是收尾最晚的那条：认早结束的动画会把亮线截断在半路。
  const riseMs = Number(CSS_SOURCE.match(/animation: request-row-rise (\d+)ms/)?.[1]);
  const tickMs = Number(CSS_SOURCE.match(/animation: request-row-edge-tick (\d+)ms/)?.[1]);
  assert.ok(Number.isFinite(riseMs) && Number.isFinite(tickMs), '两条入场动画的时长未能解析');
  assert.ok(tickMs > riseMs,
    `摘 class 认的是 ${finalName}，但它 ${tickMs}ms 并不比单元格位移 ${riseMs}ms 晚结束`);
});

test('旧的整行扫光与逐列接力已彻底移除', () => {
  // 扫光是 infinite 动画，留一条在 CSS 里就会有行永远在动。
  for (const dead of ['request-row-inflight', 'request-row-enter-cascade', 'request-row-enter-flat', 'request-cell-enter']) {
    assert.doesNotMatch(CSS_SOURCE, new RegExp(dead), `index.css 仍残留 ${dead}`);
    assert.doesNotMatch(TABLE_SOURCE, new RegExp(dead), `RequestsTable 仍引用 ${dead}`);
  }
  assert.doesNotMatch(TABLE_SOURCE, /--col-index/, '逐列接力已移除，--col-index 不应再下发');
});

test('落位动画有限次、流光无限次 —— 两者搞反表格就再也静不下来', () => {
  const declOf = (name) => CSS_SOURCE.match(new RegExp(`animation: ${name}[^;]*;`))?.[0];

  // 终态落位是一次性反馈：播完整张表必须回到完全安静。
  for (const name of ['trace-rail-fill', 'trace-rail-cap-pop', 'trace-rail-settle-flash']) {
    const decl = declOf(name);
    assert.ok(decl, `${name} 未被任何规则使用`);
    assert.doesNotMatch(decl, /infinite/, `${name} 是落位反馈，不能无限循环`);
  }

  // 进行中的流光反过来必须无限循环，否则播一轮就停在轨道外，
  // 「还在跑」这条信息会在几秒后凭空消失。
  for (const name of ['trace-rail-shoot', 'trace-rail-rewind', 'trace-rail-dot-breathe', 'trace-rail-dot-pulse']) {
    const decl = declOf(name);
    assert.ok(decl, `${name} 未被任何规则使用`);
    assert.match(decl, /infinite/, `${name} 表达「进行中」，必须无限循环`);
  }
});

test('减弱动效下进行中仍可读，且终态不会被误读成在跑', () => {
  const reducedBlock = CSS_SOURCE.match(/@media \(prefers-reduced-motion: reduce\)[\s\S]*?\n}\n/)?.[0];
  assert.ok(reducedBlock, 'reduced-motion 段落未找到');

  // 流光停下会留在轨道外，等于没有任何「进行中」标识 —— 必须有静态色带兜底
  for (const variant of IN_FLIGHT) {
    assert.match(reducedBlock, new RegExp(`trace-rail--${variant} \\.trace-rail__flow`),
      `${variant} 在减弱动效下没有静态色带兜底，状态会读不出来`);
  }
  // 终态的 fill 已经画了真实分段，再叠色带会读成「还在跑」
  for (const variant of ['completed', 'failed', 'cancelled', 'suspended']) {
    assert.doesNotMatch(reducedBlock, new RegExp(`trace-rail--${variant} \\.trace-rail__flow`),
      `${variant} 是终态，不该在减弱动效下显示流光色带`);
  }
});

test('注释里写的轨道宽度与 CSS 实际宽度一致', () => {
  // 这条不是洁癖：轨道宽度决定了状态列要占多宽，是调布局时第一个要查的数。
  // 注释写 44px 而 CSS 是 74px 的话，下次调尺寸的人会照着错的数算。
  const railBlock = CSS_SOURCE.match(/\.trace-rail \{[\s\S]*?\n\}/)?.[0];
  assert.ok(railBlock, '.trace-rail 规则未找到');
  const actualWidth = railBlock.match(/width:\s*(\d+)px/)?.[1];
  assert.ok(actualWidth, '.trace-rail 没有显式宽度');

  const mentions = [...RAIL_SOURCE.matchAll(/(\d+)px\s*轨道/g), ...CSS_SOURCE.matchAll(/(\d+)px\s*轨道/g)];
  assert.ok(mentions.length > 0, '没有一处注释提到轨道宽度，这条断言已失去意义');
  for (const [text, width] of mentions) {
    assert.equal(width, actualWidth, `注释「${text}」与 CSS 的 ${actualWidth}px 不一致`);
  }
});

test('轨道退出可访问性树 —— 状态不能被读屏念两遍', () => {
  // 轨道右边紧挨着同一状态的可见文字。给轨道再挂 aria-label，
  // 读屏会先念图形的名字再念文字，同一个状态出现两次。
  assert.match(RAIL_CODE, /aria-hidden="true"/, '轨道必须 aria-hidden');
  assert.doesNotMatch(RAIL_CODE, /role="img"/, 'aria-hidden 之后 role 已无意义，留着会误导');
  assert.doesNotMatch(RAIL_CODE, /aria-label/, '可访问名由旁边的可见文字承担');

  // 名称的唯一来源，使用与轨道一致的展示分类，删掉文字就只剩颜色编码了
  const traceCell = RAIL_CODE.slice(RAIL_CODE.indexOf('export const RequestTraceCell'));
  assert.match(traceCell, /getRequestPresentation\(request\)/,
    'RequestTraceCell 必须使用统一的展示分类');
  assert.match(traceCell, /getStatusConfig\(status\)\.label/,
    'RequestTraceCell 必须保留可见状态文字');
});

test('状态列排在首位 —— 轨道是这张表的扫描锚点', () => {
  assert.equal(TABLE_COLUMNS[0].id, 'status',
    '状态被挤到后面，轨道就会被长请求 ID 和时间戳隔断，动态感读不出来');
});

test('表头与数据格同源 —— 否则表头会盖在错误的数据列上', () => {
  // 曾经 <td> 遍历 visibleColumns（state 数组）、<thead> 遍历 TABLE_COLUMNS，
  // 两者对得上纯粹因为默认数组被手抄成了同序。TABLE_COLUMNS 一调顺序就整体错位，
  // 而且勾一下任意列就会自愈（toggleColumn 会按 TABLE_COLUMNS 重排），只在首屏错。
  assert.match(TABLE_CODE, /columns=\{visibleColumnConfigs\}/,
    '数据行必须收表头用的那一份列配置');
  assert.doesNotMatch(TABLE_CODE, /visibleColumns\.map\(/,
    '<td> 不能再按 state 数组的顺序渲染');

  // defaultHidden 列不进入默认可见列，派生时仍保持 TABLE_COLUMNS 的顺序。
  assert.deepEqual(DEFAULT_VISIBLE_COLUMNS, TABLE_COLUMNS.filter(col => !col.defaultHidden).map(col => col.id),
    '默认可见列必须派生自 TABLE_COLUMNS，手抄的版本会漏列、写错 id、失配顺序');
  assert.ok(!DEFAULT_VISIBLE_COLUMNS.includes('sessionId'),
    '会话列默认隐藏，不应进入默认可见列');
});

test('右对齐只有一份定义 —— 不靠列 id 猜', () => {
  assert.match(TABLE_CODE, /col\.align === 'right'/, '对齐应该读列配置');
  assert.doesNotMatch(TABLE_CODE, /colId\.includes\('Tokens'\)/,
    'align 已经在 TABLE_COLUMNS 里，再按 id 猜就是同一份信息抄了两遍');
});

test('状态列宽度写死 —— 它一抖后面每一列都跟着右移', () => {
  // table-layout:auto 下列宽取当页内容的最大值。状态列现在排首位，
  // 而 getStatusConfig 的兜底会把未知状态的 label 退化成后端原始串。
  assert.match(RAIL_CODE, /inline-flex w-\[\d+px\]/,
    'RequestTraceCell 外层必须有固定宽度');
  assert.match(RAIL_CODE, /truncate/,
    '标签必须能被截住，否则未知状态的原始串会把整列撑开');
});
