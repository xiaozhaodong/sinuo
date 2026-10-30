import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

import { IN_FLIGHT_STATUSES } from './constants.js';
import {
  TIMING_THRESHOLDS_MS,
  PENDING_SEND_THRESHOLDS_MS,
  getTimingPillClassName,
  getRunningPillClassName,
  getPendingSendPillState,
  formatTimingBadge
} from './timing.js';
import {
  subscribeElapsedTick,
  resolveElapsedMs,
  resolveGenerationTickAnchor,
  __getTickerState,
  TICK_INTERVAL_MS
} from './elapsedTicker.js';

const HERE = dirname(fileURLToPath(import.meta.url));
const readSource = (relativePath) => readFileSync(join(HERE, relativePath), 'utf8');

const PILL_SOURCE = readSource('../components/LiveElapsedPill.jsx');
const TABLE_SOURCE = readSource('../components/RequestTimingCell.jsx');
const PENDING_SOURCE = readSource('../components/PendingSendPill.jsx');
const CSS_SOURCE = readSource('../../../index.css');

test('订阅者归零时定时器被清掉 —— 没有进行中的请求，整张表零定时器', async () => {
  assert.deepEqual(__getTickerState(), { subscriberCount: 0, running: false });

  const unsubA = subscribeElapsedTick(() => {});
  assert.deepEqual(__getTickerState(), { subscriberCount: 1, running: true });

  const unsubB = subscribeElapsedTick(() => {});
  assert.deepEqual(__getTickerState(), { subscriberCount: 2, running: true },
    '第二个订阅者不该再起一个定时器');

  unsubA();
  assert.equal(__getTickerState().running, true, '还有订阅者时不能停');

  unsubB();
  assert.deepEqual(__getTickerState(), { subscriberCount: 0, running: false },
    '最后一个订阅者退订后必须清掉定时器，否则表格永远醒着');
});

test('一个订阅者抛错不会让其余的时钟停摆', async () => {
  const seen = [];
  const originalError = console.error;
  console.error = () => {};

  const unsubBad = subscribeElapsedTick(() => { throw new Error('boom'); });
  const unsubGood = subscribeElapsedTick(() => seen.push(1));

  await new Promise((resolve) => setTimeout(resolve, TICK_INTERVAL_MS * 2.5));

  unsubBad();
  unsubGood();
  console.error = originalError;

  assert.ok(seen.length >= 1, '正常订阅者应该照常收到 tick');
  assert.equal(__getTickerState().running, false);
});

test('跑秒精度必须细于显示精度，否则数字会跳格', () => {
  // formatTimingBadge 显示到 0.01s；降级间隔慢于一帧就会一次跳过多个刻度。
  assert.ok(TICK_INTERVAL_MS <= 16, `刷新间隔 ${TICK_INTERVAL_MS}ms 粗于一帧`);
});

test('跑秒与定稿共用同一把尺子 —— 完成瞬间精度不能变', () => {
  // 精度在完成瞬间跳变会让人以为数据源换了；「完成」由灰→彩负责表达，
  // 不需要数字帮腔。所以 LiveElapsedPill 和静态徽章必须是同一个函数。
  assert.equal(formatTimingBadge(5432), '5.43s');
  assert.match(PILL_SOURCE, /formatTimingBadge\(elapsedMs\)/,
    '跑秒不该有自己的格式函数');

  // 三档宽度一致，跑秒过程中列宽不抖。
  for (const ms of [999, 1000, 5432, 10000]) {
    assert.equal(formatTimingBadge(ms).length, 5, `${ms} 的宽度应与其他档一致`);
  }
});

test('已耗时钳到非负 —— 时间戳异常时不显示负数', () => {
  assert.equal(resolveElapsedMs(1000, 0, 3500), 2500);
  assert.equal(resolveElapsedMs(1000, 400, 3500), 2100, 'baseMs 应从已耗时里扣掉');
  assert.equal(resolveElapsedMs(5000, 0, 3500), 0, '起点晚于当前时刻时钳到 0');
  assert.equal(resolveElapsedMs(1000, 9999, 3500), 0, 'baseMs 大于已耗时时钳到 0');
});

test('跑秒走 DOM 直写，不进 React 渲染状态', () => {
  // 每 100ms 一次 setState 会推全表 diff，而那正是新行入场动画在跑的时刻。
  assert.match(PILL_SOURCE, /node\.textContent = /, '应直接写 textContent');
  assert.doesNotMatch(PILL_SOURCE, /useState/, '跑秒不能持有 React state');
});

test('色阶跟着跑秒一起走 —— 卡住的请求会自己变黄变红', () => {
  assert.match(PILL_SOURCE, /node\.className = /,
    '只更新文本不更新 className 的话，等首字等到 30s 也还是绿的');

  // 首响位与生成位用不同阈值：等首字 8s 就该警示，生成 8s 完全正常。
  assert.ok(TIMING_THRESHOLDS_MS.first.warning < TIMING_THRESHOLDS_MS.duration.warning);
  assert.equal(getTimingPillClassName('first', TIMING_THRESHOLDS_MS.first.warning + 1), 'tone-amber');
  assert.equal(getTimingPillClassName('first', TIMING_THRESHOLDS_MS.first.critical + 1), 'tone-rose');
  assert.equal(getTimingPillClassName('duration', TIMING_THRESHOLDS_MS.first.critical + 1), 'tone-emerald',
    '生成位不该套用首响的阈值');
});

test('跑秒落在当前正在进行的那一段上', () => {
  // 首字未到 -> 首响位从上游请求写完开始跑（threshold="first"，无 baseMs）；还没写完只放等待点
  assert.match(TABLE_SOURCE, /<LiveElapsedPill timestamp=\{request\.firstTokenStartAt\} threshold="first" \/>/);
  assert.match(TABLE_SOURCE, /firstResponseWait\(request\)[\s\S]{0,100}<PendingSendPill \{\.\.\.waiting\} \/>/);
  // 生成耗时随事件先到时计时格先定格，不等状态刷新
  assert.match(TABLE_SOURCE, /timingRunning = running && !Number\.isFinite\(request\.completionMs\)/);
  // 首字已到 -> 生成位接过跑秒，起算点由 resolveGenerationTickAnchor 给出
  assert.match(TABLE_SOURCE, /\.\.\.resolveGenerationTickAnchor\(request, firstResponseMs\)\}[\s\S]{0,40}threshold="duration"/);
});

test('等待点 1 秒内静止，超过 1 秒才动，再久变黄 / 变红', () => {
  assert.deepEqual(getPendingSendPillState(0), { tone: 'tone-slate', animate: false });
  assert.deepEqual(getPendingSendPillState(PENDING_SEND_THRESHOLDS_MS.animate - 1), { tone: 'tone-slate', animate: false },
    '大多零点几秒就发出去了，一圈动画都走不完，动了像闪');
  assert.deepEqual(getPendingSendPillState(PENDING_SEND_THRESHOLDS_MS.animate), { tone: 'tone-slate', animate: true });
  assert.deepEqual(getPendingSendPillState(PENDING_SEND_THRESHOLDS_MS.warning + 1), { tone: 'tone-amber', animate: true });
  assert.deepEqual(getPendingSendPillState(PENDING_SEND_THRESHOLDS_MS.critical + 1), { tone: 'tone-rose', animate: true });
  assert.deepEqual(getPendingSendPillState(Number.NaN), { tone: 'tone-slate', animate: false }, '时间异常按刚进入处理');
});

test('等待点与跑秒共用时钟、直写 className，动画只在超过 1 秒的阶段挂上', () => {
  assert.match(PENDING_SOURCE, /subscribeElapsedTick\(paint\)/);
  assert.match(PENDING_SOURCE, /node\.className = className/);
  assert.doesNotMatch(PENDING_SOURCE, /useState/, '阶段切换不进 React 渲染状态');
  assert.match(CSS_SOURCE, /\.pending-send-dots--waiting > i \{\s*animation: pending-send-dot [^;]*infinite;/);
  assert.match(CSS_SOURCE, /@media \(prefers-reduced-motion: reduce\) \{[\s\S]*?\.pending-send-dots--waiting > i,[\s\S]*?animation: none;/,
    '减弱动效时等待点不动，只靠变色示警');
});

test('生成位优先从首字到达时刻起算，与定稿的 completion_ms 同一起点', () => {
  const request = {
    timestamp: '2026-10-11T02:00:00.000000Z',
    firstTokenAt: '2026-10-11T02:00:03.500000Z'
  };
  assert.deepEqual(resolveGenerationTickAnchor(request, 3000),
    { timestamp: '2026-10-11T02:00:03.500000Z', baseMs: 0 },
    '有首字时刻时不再用「开始 + 首响」，否则会把本地处理与连接多算进生成位');

  // 3.5s 时首字到达、再过 2s：生成位应显示 2s，而不是旧算法的 2.5s
  const anchor = resolveGenerationTickAnchor(request, 3000);
  const startedAt = Date.parse(anchor.timestamp);
  assert.equal(resolveElapsedMs(startedAt, anchor.baseMs, Date.parse('2026-10-11T02:00:05.500000Z')), 2000);
});

test('没有首字时刻时退回「请求开始 + 首响」', () => {
  for (const firstTokenAt of [undefined, '', 'not-a-time']) {
    assert.deepEqual(
      resolveGenerationTickAnchor({ timestamp: '2026-10-11T02:00:00.000000Z', firstTokenAt }, 3000),
      { timestamp: '2026-10-11T02:00:00.000000Z', baseMs: 3000 }
    );
  }
});

test('进行中不吃 resolveFirstResponseMs 的非流式降级', () => {
  // 那条降级拿 duration 兜底首响，前提是「请求已结束」。
  // 进行中 duration 恒为 0，兜底会让每条非流式请求先显示一个假的 0.0s。
  assert.match(TABLE_SOURCE, /running\s*\?\s*\(Number\.isFinite\(request\.firstTokenMs\)/,
    '进行中必须只认后端真实写入的 firstTokenMs');
});

test('suspended 不跑秒 —— 与轨道对同一份集合判定', () => {
  assert.equal(IN_FLIGHT_STATUSES.has('suspended'), false,
    '挂起的请求已经停了，跑秒会暗示它还在工作');
  for (const status of ['pending', 'forwarding', 'processing', 'retry']) {
    assert.equal(IN_FLIGHT_STATUSES.has(status), true, `${status} 必须跑秒`);
  }
});

test('跑秒正常区间是中性灰 —— 给完成瞬间留出「灰→彩」', () => {
  // 收敛成一条规则：灰 = 这一段还在跑，彩 = 这一段定了。
  // 不这么分的话，进行中和已完成都是两枚绿 pill，秒表停下来根本看不出来。
  assert.equal(getRunningPillClassName('first', 1000), 'tone-slate');
  assert.equal(getRunningPillClassName('duration', 1000), 'tone-slate');
  assert.equal(getTimingPillClassName('duration', 1000), 'tone-emerald',
    '定稿的同一个数字必须是绿的，完成瞬间就靠这个差别被看见');
});

test('跑秒的示警区间不受中性化影响 —— 卡住比未定稿更该被看见', () => {
  const { first, duration } = TIMING_THRESHOLDS_MS;
  assert.equal(getRunningPillClassName('first', first.warning + 1), 'tone-amber');
  assert.equal(getRunningPillClassName('first', first.critical + 1), 'tone-rose');
  assert.equal(getRunningPillClassName('duration', duration.warning + 1), 'tone-orange');
  assert.equal(getRunningPillClassName('duration', duration.critical + 1), 'tone-rose');

  assert.match(PILL_SOURCE, /getRunningPillClassName\(threshold, elapsedMs\)/,
    '跑秒必须走 running 色阶，用 getTimingPillClassName 会提前把绿色用掉');
});
