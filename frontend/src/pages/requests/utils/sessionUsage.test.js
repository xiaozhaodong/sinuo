import test from 'node:test';
import assert from 'node:assert/strict';
import { isInputSpike, formatSessionCost, formatSessionRatio, sessionDrillQuery, requireLocatedRequest, sessionKey } from './sessionUsage.js';

test('突增需翻倍且至少增加10000，前值必须已知且大于零', () => {
  for (const [previous, full_input, expected] of [[28000,196000,true],[10000,20000,true],[9999,19998,false],[0,20000,false],[null,20000,false],[10000,null,false],[10000,19999,false]]) {
    assert.equal(isInputSpike({ previous, full_input }), expected);
  }
});

test('真实零与未知独立显示，同ID不同请求族独立选中', () => {
  assert.equal(formatSessionCost(0), '$0.0000');
  assert.equal(formatSessionCost(null), '—');
  assert.equal(formatSessionRatio(0), '0.0%');
  assert.equal(formatSessionRatio(null), '—');
  assert.notEqual(sessionKey({ request_family:'claude',session_id:'same' }), sessionKey({ request_family:'codex',session_id:'same' }));
});

test('下钻保留筛选并传完整会话和目标ID，实际页必须包含目标行', () => {
  const query = sessionDrillQuery({ model:'m', start_date:'start', include_compaction:false, source_view:'account', session_missing:true }, { request_family:'codex',session_id:'full-session-id' }, 'old-request');
  assert.deepEqual(query, { model:'m',start_date:'start',include_compaction:false,source_view:'account',session_missing:false,request_family:'codex',session_id:'full-session-id',target_request_id:'old-request' });
  assert.throws(() => requireLocatedRequest({ requests: [{requestId:'new-request'}], page:1 }, 'old-request'), /列表已变化/);
  const row = {requestId:'old-request'};
  assert.equal(requireLocatedRequest({requests:[row],page:4}, 'old-request'), row);
});
