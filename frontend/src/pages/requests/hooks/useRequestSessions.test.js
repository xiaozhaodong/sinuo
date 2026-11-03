import test from 'node:test';
import assert from 'node:assert/strict';
import { createSessionQueryController } from '../utils/sessionUsage.js';
import { build } from 'esbuild';
import { fileURLToPath } from 'node:url';

const deferred = () => { let resolve, reject; const promise = new Promise((r,j) => { resolve=r;reject=j; }); return {promise,resolve,reject}; };
test('快速切换会话或筛选时，旧走势和旧错误均不能抢回选择', async () => {
  const controller = createSessionQueryController();
  const old = deferred(); const fresh = deferred(); const applied=[]; const errors=[];
  const first = controller.run('trend', () => old.promise, (x) => applied.push(x), (e) => errors.push(e));
  const second = controller.run('trend', () => fresh.promise, (x) => applied.push(x));
  fresh.resolve('new-session'); await second;
  old.reject(new Error('obsolete')); await first;
  assert.deepEqual(applied,['new-session']); assert.deepEqual(errors,[]);
});
test('刷新不抢占定位，取消或卸载使等待中的操作失效', async () => {
  const controller = createSessionQueryController(); const pending=deferred(); const applied=[];
  const location=controller.run('location',()=>pending.promise, (x)=>applied.push(x));
  await controller.run('ranking',async()=> 'ranking', (x)=>applied.push(x));
  controller.cancel('location'); pending.resolve('stale-location'); await location;
  assert.deepEqual(applied,['ranking']);
});

// 执行实际 Hook，只替换 React 调度器和网络依赖；状态转移与回调均来自生产代码。
// 无需给项目新增 DOM 测试依赖，浏览器联调另外验证实际 React 提交与滚动。
let hookModule;
const loadHook = async () => {
  if (!hookModule) {
    const compiled = await build({ entryPoints:[fileURLToPath(new URL('./useRequestSessions.js',import.meta.url))], bundle:true, write:false, platform:'node',format:'esm',
      plugins:[{name:'actual-session-hook',setup(builder){
        builder.onResolve({filter:/^(react|@utils\/api\.js|@utils\/wailsApi\.js)$/},({path})=>({path,namespace:'hook-test'}));
        builder.onLoad({filter:/.*/,namespace:'hook-test'},({path})=>({contents:path==='react'
          ? ['useState','useRef','useMemo','useCallback','useEffect'].map(name=>`export const ${name}=(...args)=>globalThis.__sessionHookHost.${name}(...args);`).join('\n')
          : path.includes('wailsApi') ? 'export const isWailsEnvironment=()=>true;'
            : ['fetchRequests','fetchRequestSessions','fetchRequestSessionTrend'].map(name=>`export const ${name}=(...args)=>globalThis.__sessionHookHost.api.${name}(...args);`).join('\n')}));
      }}] });
    hookModule=await import(`data:text/javascript;base64,${Buffer.from(compiled.outputFiles[0].text).toString('base64')}`);
  }
  return hookModule.default;
};

const sameDeps=(left,right)=>left&&right&&left.length===right.length&&left.every((value,i)=>Object.is(value,right[i]));
const createHookHarness = async (t, activeRange='today') => {
  const hook=await loadHook(); const slots=[]; let cursor=0, pending=false, disposed=false, effects=[];
  const rows=['a','b'].map(id=>({request_family:'codex',session_id:`session-${id}`,request_count:2}));
  const points=(session)=>[1,2].map(n=>({request_id:`${session}-${n}`,ordinal:n,full_input:100*n,previous:n===1?null:100}));
  const dates=activeRange==='custom'
    ? {startDate:'2026-11-01T05:30:00.123456Z',endDate:'2026-11-03T03:45:00.654321Z'}
    : {startDate:'2026-11-03T00:00:00',endDate:'2026-11-04T00:00:00'};
  const original={filters:dates,query:{start_date:dates.startDate,end_date:dates.endDate}};
  const parent={...original,activeRange,requests:[],pagination:{page:1,pageSize:30},selection:null,notices:[]};
  const calls=[];
  const stateSlot=(init)=>{ const index=cursor++; if(!slots[index]) slots[index]=init(); return slots[index]; };
  const host={parent,rows,calls,original,api:{
    fetchRequestSessions:async()=>({rows,total:2,page:1,page_size:30}),
    fetchRequestSessionTrend:async(params,selectedID)=>({points:points(params.session_id),total:2,page:1,page_size:100,selected_id:selectedID||`${params.session_id}-1`}),
    fetchRequests:async(params)=>{calls.push(params);return{requests:[{requestId:params.target_request_id}],page:4,pageSize:30,total:100,totalPages:4};}
  }};
  const schedule=()=>{if(disposed||pending)return;pending=true;queueMicrotask(()=>{pending=false;if(!disposed)render();});};
  const setParent=(key,value)=>{parent[key]=typeof value==='function'?value(parent[key]):value;schedule();};
  const props={pageSize:30,updateFilters:patch=>setParent('filters',{...parent.filters,...patch}),setQuery:value=>setParent('query',value),setPagination:value=>setParent('pagination',value),setRequests:value=>setParent('requests',value),setSelection:value=>setParent('selection',value),showDetail:()=>{},showNotice:(...args)=>parent.notices.push(args),restoreTimeRange:value=>setParent('activeRange',value)};
  Object.assign(host,{
    useState(init){const item=stateSlot(()=>{const item={value:typeof init==='function'?init():init};item.set=value=>{const next=typeof value==='function'?value(item.value):value;if(!Object.is(next,item.value)){item.value=next;schedule();}};return item;});return[item.value,item.set];},
    useRef(init){return stateSlot(()=>({current:init}));},
    useMemo(fn,deps){const item=stateSlot(()=>({}));if(!sameDeps(item.deps,deps)){item.value=fn();item.deps=deps;}return item.value;},
    useCallback(fn,deps){return host.useMemo(()=>fn,deps);},
    useEffect(fn,deps){const item=stateSlot(()=>({}));if(!sameDeps(item.deps,deps)){item.deps=deps;effects.push(()=>{item.cleanup?.();item.cleanup=fn();});}}
  });
  const render=()=>{globalThis.__sessionHookHost=host;cursor=0;effects=[];host.current=hook({...props,filters:parent.filters,query:parent.query,activeRange:parent.activeRange});const commits=effects;effects=[];for(const effect of commits)effect();};
  host.flush=async()=>{for(let i=0;i<20;i++)await Promise.resolve();};
  host.changeParent=(patch)=>{Object.assign(parent,patch);render();};
  render();
  t.after(()=>{disposed=true;for(const item of slots)item.cleanup?.();});
  host.current.changeMode('sessions');await host.flush();await host.current.refresh();await host.flush();
  return host;
};

const pagedTrend = (page=1) => {
  const ordinal=page===1?5:101;
  return {points:[{request_id:`session-a-${ordinal}`,ordinal,full_input:28000,previous:14000}],total:115,page,page_size:100,selected_id:`session-a-${ordinal}`};
};

for (const first of ['后台','翻页']) test(`实际Hook：后台走势与翻页交错，${first}先返回仍停留第2页`,async(t)=>{
  const h=await createHookHarness(t); const background=deferred(),page=deferred();
  h.api.fetchRequestSessionTrend=(_params,_id,nextPage)=>nextPage===2?page.promise:background.promise;
  await h.current.refresh(); await h.flush();
  const flipping=h.current.loadTrend(2); await h.flush();
  if(first==='后台') { background.resolve(pagedTrend(1));await h.flush();page.resolve(pagedTrend(2)); }
  else { page.resolve(pagedTrend(2));await h.flush();background.resolve(pagedTrend(1)); }
  await flipping; await h.flush();
  assert.equal(h.current.trend.page,2); assert.equal(h.current.point.request_id,'session-a-101');
});

for (const first of ['排行','翻页']) test(`实际Hook：用户翻页期间排行刷新，${first}先返回仍停留第2页`,async(t)=>{
  const h=await createHookHarness(t); const ranking=deferred(),page=deferred(); const calls=[];
  h.api.fetchRequestSessions=()=>ranking.promise;
  h.api.fetchRequestSessionTrend=(_params,id,nextPage)=>{
    calls.push({id,page:nextPage});
    return nextPage===2?page.promise:Promise.resolve(pagedTrend(id==='session-a-101'?2:1));
  };
  const flipping=h.current.loadTrend(2); await h.flush();
  const refreshing=h.current.refresh();
  if(first==='排行') {
    ranking.resolve({rows:h.rows.map(row=>({...row})),total:2});await refreshing;await h.flush();
    assert.equal(calls.length,1,'用户翻页未完成时后台不得按旧选中点发起走势');
    page.resolve(pagedTrend(2));
  } else {
    page.resolve(pagedTrend(2));await flipping;await h.flush();
    ranking.resolve({rows:h.rows.map(row=>({...row})),total:2});await refreshing;
  }
  await flipping;await h.flush();
  assert.equal(h.current.trend.page,2);assert.equal(h.current.point.request_id,'session-a-101');
});

for (const action of ['排序','刷新']) for (const state of ['保留','移出','空页']) test(`实际Hook：${action}后选中会话${state}，高亮、走势和定位只使用当前页`,async(t)=>{
  const h=await createHookHarness(t);
  h.current.select(h.rows[1]);await h.flush();
  const nextRows=state==='保留'?[{...h.rows[1],request_count:3},h.rows[0]]:state==='移出'
    ? ['c','d'].map(id=>({request_family:'codex',session_id:`session-${id}`,request_count:2})):[];
  h.api.fetchRequestSessions=async()=>({rows:nextRows,total:state==='空页'?0:4});
  if(action==='排序') {
    h.current.sort('cost');await h.flush();
    assert.equal(h.current.selected,null);assert.equal(h.current.point,null);
    await h.current.navigate();assert.equal(h.calls.length,0,'新排行未返回时不能下钻旧会话');
  }
  await h.current.refresh();await h.flush();
  const wanted=nextRows[0]?.session_id||null;
  assert.equal(h.current.selected?.session_id||null,wanted);
  assert.equal(h.current.point?.request_id||null,wanted?`${wanted}-1`:null);
  if(state==='保留') assert.equal(h.current.selected.request_count,3,'不能保留旧行对象');
  if(state==='移出') {
    h.api.fetchRequestSessions=async()=>({rows:[...nextRows,h.rows[1]],total:3});
    await h.current.refresh();await h.flush();
    assert.equal(h.current.selected.session_id,wanted,'旧会话重新出现不应抢回选中');
  }
  await h.current.navigate();await h.flush();
  if(wanted) assert.equal(h.parent.selection.requestId,`${wanted}-1`);
  else assert.equal(h.calls.length,0);
});

test('实际Hook：刷新相同请求不发出滚动意图，主动重选同一点仍发出滚动意图',async(t)=>{
  const h=await createHookHarness(t);const point=h.current.point;
  h.current.choosePoint(point);await h.flush();const revision=h.current.pointFocusRevision;
  await h.current.refresh();await h.flush();
  assert.notEqual(h.current.point,point);assert.equal(h.current.point.request_id,point.request_id);
  assert.equal(h.current.pointFocusRevision,revision);
  h.current.choosePoint(h.current.point);await h.flush();
  assert.equal(h.current.pointFocusRevision,revision+1);
});

for(const missing of [false,true]) test(`实际Hook：${missing?'无ID':'会话'}下钻清除条件后隐藏旧说明且保留返回入口`,async(t)=>{
  const h=await createHookHarness(t);await h.current.navigate(missing);await h.flush();
  assert.equal(h.current.returnAvailable,true);
  assert.equal(h.current.returnDescription,missing?'正在查看无会话 ID 的请求':'已按请求族与完整会话 ID 筛选');
  h.changeParent({query:{...h.original.query},filters:{...h.original.filters}});await h.flush();
  assert.equal(h.current.returnDescription,null);assert.equal(h.current.returnAvailable,true);
  h.current.changeMode('sessions');await h.flush();
  assert.deepEqual(h.parent.query,h.original.query);assert.equal(h.current.mode,'sessions');
});

test('实际Hook：初始最近活动降序，手动成本排序与下钻返回后保持',async(t)=>{
  const h=await createHookHarness(t);
  assert.equal(h.current.sortBy,'recent'); assert.equal(h.current.sortOrder,'desc');
  let query;
  h.api.fetchRequestSessions=async(params)=>{query=params;return{rows:h.rows,total:2};};
  await h.current.refresh(); await h.flush();
  assert.equal(query.sort_by,'recent'); assert.equal(query.sort_order,'desc');
  h.current.sort('cost'); await h.flush();
  await h.current.refresh(); await h.flush();
  assert.equal(query.sort_by,'cost'); assert.equal(query.sort_order,'desc');
  h.current.select(h.rows[1]); await h.flush();
  await h.current.navigate(); await h.flush();
  h.current.changeMode('sessions'); await h.flush();
  assert.equal(h.current.sortBy,'cost'); assert.equal(h.current.sortOrder,'desc');
  assert.equal(h.current.selected.session_id,'session-b');
});

for (const [name,change] of [
  ['会话',h=>h.current.select(h.rows[1])],
  ['走势点',h=>h.current.choosePoint(h.current.trend.points[1])],
  ['排行页',h=>h.current.changePage(2)],
  ['排行排序',h=>h.current.sort('count')],
  ['每页条数',h=>h.current.changeSize(20)],
  ['走势页',h=>h.current.loadTrend(2)]
]) for (const failed of [false,true]) test(`实际Hook：切换${name}后旧定位${failed?'失败':'成功'}不回写`,async(t)=>{
  const h=await createHookHarness(t);const request=deferred();h.api.fetchRequests=()=>request.promise;
  const navigating=h.current.navigate();await h.flush();assert.equal(h.current.locating,true);
  await change(h);await h.flush();
  if(failed)request.reject(new Error('旧定位错误'));
  else request.resolve({requests:[{requestId:'session-a-1'}],page:4,pageSize:30,total:100,totalPages:4});
  await navigating;await h.flush();
  assert.equal(h.current.mode,'sessions');assert.equal(h.parent.selection,null);assert.deepEqual(h.parent.query,h.original.query);
  assert.deepEqual(h.parent.notices,[]);assert.equal(h.current.locating,false);
});

test('实际Hook：target_request_id 只用于首次定位，常规查询保留页码和会话',async(t)=>{
  const h=await createHookHarness(t);await h.current.navigate();await h.flush();
  assert.equal(h.calls[0].target_request_id,'session-a-1');
  assert.equal('target_request_id' in h.parent.query,false);
  assert.equal(h.parent.query.session_id,'session-a');assert.equal(h.parent.query.request_family,'codex');
  assert.equal(h.parent.pagination.page,4);assert.equal(h.parent.selection.requestId,'session-a-1');
});

for (const failed of [false,true]) test(`实际Hook：旧定位${failed?'失败':'成功'}不结束新选择的定位状态`,async(t)=>{
  const h=await createHookHarness(t);const old=deferred(),current=deferred();let index=0;
  h.api.fetchRequests=()=>index++===0?old.promise:current.promise;
  const first=h.current.navigate();await h.flush();
  h.current.select(h.rows[1]);await h.flush();
  const second=h.current.navigate();await h.flush();
  if(failed)old.reject(new Error('旧请求失败'));
  else old.resolve({requests:[{requestId:'session-a-1'}],page:4,pageSize:30});
  await first;await h.flush();
  assert.equal(h.current.locating,true);assert.equal(h.current.mode,'sessions');assert.deepEqual(h.parent.notices,[]);
  current.resolve({requests:[{requestId:'session-b-1'}],page:2,pageSize:30,total:50,totalPages:2});
  await second;await h.flush();
  assert.equal(h.current.locating,false);assert.equal(h.parent.selection.requestId,'session-b-1');assert.equal(h.parent.pagination.page,2);
});

test('实际Hook：当前定位错误仍然显示，筛选变化后的旧错误不显示',async(t)=>{
  const h=await createHookHarness(t);
  h.api.fetchRequests=async()=>{throw new Error('当前目标已不存在');};
  await h.current.navigate();await h.flush();
  assert.deepEqual(h.parent.notices,[['error','当前目标已不存在']]);assert.equal(h.current.locating,false);
  const old=deferred();h.api.fetchRequests=()=>old.promise;const navigating=h.current.navigate();await h.flush();
  h.changeParent({query:{...h.parent.query,model:'new-model'}});
  old.reject(new Error('旧筛选错误'));await navigating;await h.flush();
  assert.deepEqual(h.parent.notices,[['error','当前目标已不存在']]);assert.equal(h.current.mode,'sessions');
});

for (const failed of [false, true]) test(`实际Hook：模型筛选往返后旧定位${failed ? '失败' : '成功'}不恢复等待状态且可以重新定位`, async (t) => {
  const h = await createHookHarness(t);
  h.current.select(h.rows[0]);
  await h.flush();
  h.current.choosePoint(h.current.trend.points[1]);
  await h.flush();
  const selectedKey = h.current.selectedKey;
  const requestID = h.current.point.request_id;
  const old = deferred();
  h.api.fetchRequests = () => old.promise;
  const navigating = h.current.navigate();
  await h.flush();
  assert.equal(h.current.locating, true);

  h.changeParent({
    filters: { ...h.original.filters, model: 'other-model' },
    query: { ...h.original.query, model: 'other-model' }
  });
  await h.current.refresh();
  await h.flush();
  assert.equal(h.current.locating, false);
  if (failed) old.reject(new Error('已失效的模型范围定位错误'));
  else old.resolve({ requests: [{ requestId: requestID }], page: 4, pageSize: 30, total: 100, totalPages: 4 });
  await navigating;
  await h.flush();

  h.changeParent({ filters: h.original.filters, query: h.original.query });
  await h.current.refresh();
  await h.flush();
  assert.equal(h.current.mode, 'sessions');
  assert.equal(h.current.selectedKey, selectedKey);
  assert.equal(h.current.point.request_id, requestID);
  assert.equal(h.current.locating, false, '旧范围的等待状态不能在返回同一请求点时复活');
  assert.deepEqual(h.parent.notices, []);

  h.api.fetchRequests = async (params) => {
    assert.equal(params.target_request_id, requestID);
    return { requests: [{ requestId: requestID }], page: 4, pageSize: 30, total: 100, totalPages: 4 };
  };
  await h.current.navigate();
  await h.flush();
  assert.equal(h.current.mode, 'requests');
  assert.equal(h.parent.selection.requestId, requestID);
  assert.equal(h.current.locating, false);
  assert.deepEqual(h.parent.notices, []);
});

for (const activeRange of ['today','custom']) test(`实际Hook：返回排行恢复 ${activeRange} 预设和原始边界`,async(t)=>{
  const h=await createHookHarness(t,activeRange);await h.current.navigate();await h.flush();
  h.changeParent({activeRange:'7days',filters:{startDate:'2026-10-28T00:00:00',endDate:'2026-11-04T00:00:00'},query:{start_date:'2026-10-28T00:00:00',end_date:'2026-11-04T00:00:00'}});
  h.current.changeMode('sessions');await h.flush();
  assert.equal(h.parent.activeRange,activeRange);assert.deepEqual(h.parent.query,h.original.query);assert.deepEqual(h.parent.filters,h.original.filters);
});
