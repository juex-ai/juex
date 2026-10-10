import { createRequire } from 'node:module';
const require=createRequire(new URL('../../../frontend/package.json',import.meta.url));
const {expect,test}=require('@playwright/test');

test('expanded tool streams original output, preserves errors and stops polling when hidden',async({page})=>{
  const user={id:'owner',email:'owner@example.test',email_verified:true};
  const thread={id:'main',agent_id:'agent',kind:'main',name:'Main',retention:'active',state:'idle',generation:1,sequence:2,pending_inputs:0,held_inputs:0};
  const call={type:'tool_use',tool_use_id:'call',tool_name:'exec_command',input:{command:'long command'}};
  const result={type:'tool_result',tool_use_id:'call',tool_name:'exec_command',content:JSON.stringify({handle:{environment_id:'device',operation_id:'operation'},state:'running',output:'initial'})};
  const events=[{id:'a',sequence:1,kind:'message.appended',data:{id:'attempt',role:'assistant',blocks:[call]}},{id:'b',sequence:2,kind:'tool.ready',tool_attempt_id:'attempt',data:{id:'operation',call,result}}];
  let reads=0, failOnce=false, finished=false;
  await page.route('**/api/**',async route=>{
    const url=new URL(route.request().url()),path=url.pathname;
    const json=value=>route.fulfill({contentType:'application/json',body:JSON.stringify(value)});
    if(path==='/api/auth/session')return json(user);
    if(path==='/api/config')return json({email_enabled:false});
    if(path==='/api/tenants')return json([{id:'tenant',name:'Workspace',role:'member'}]);
    if(path.endsWith('/notifications'))return json({items:[],unread:0});
    if(path.endsWith('/agents/agent'))return json({agent:{id:'agent',name:'Assistant',status:'active',worker_depth:1},owner_id:user.id,can_execute:true});
    if(path.endsWith('/threads'))return json([thread]);
    if(path.endsWith('/events'))return json({thread,events:events.filter(event=>event.sequence>Number(url.searchParams.get('after')||0)),next_sequence:2,has_more:false});
    if(path.endsWith('/output')){
      reads++;
      const after=Number(url.searchParams.get('after'));
      if(after>0&&failOnce){failOnce=false;return route.fulfill({status:503,body:'{}'});}
      const all=finished?Buffer.from('phase one\n中文 phase two\n'):Buffer.concat([Buffer.from('phase one\n'),Buffer.from([0xe4])]);
      const output=all.subarray(after);
      return json({id:'operation',environment_id:'device',state:finished?'completed':'running',output:output.toString('base64'),next_cursor:all.length,output_bytes:all.length,truncated:false,output_expired:false,exit_code:finished?0:null});
    }
    return route.fulfill({status:404,body:'{}'});
  });
  await page.goto('/t/tenant/agents/agent');
  await expect(page.getByText('工作过程',{exact:true})).toBeVisible();
  expect(reads).toBe(0);
  await page.getByText('工作过程',{exact:true}).click();
  await page.getByText('exec_command',{exact:true}).click();
  const output=page.getByRole('region',{name:'原操作实时输出'});
  await expect(output).toContainText('phase one');
  await expect(output).not.toContainText('�');
  await page.getByText('工作过程',{exact:true}).click();
  await expect(output).toHaveCount(0);
  const hiddenCount=reads;
  await page.waitForResponse(response=>response.url().includes('/events?after='));
  await page.waitForResponse(response=>response.url().includes('/events?after='));
  expect(reads).toBe(hiddenCount);
  failOnce=true;
  await page.getByText('工作过程',{exact:true}).click();
  await expect(page.getByRole('button',{name:'重试读取输出'})).toBeVisible();
  await expect(output).toContainText('phase one');
  finished=true;
  await page.getByRole('button',{name:'重试读取输出'}).click();
  await expect(output).toContainText('中文 phase two');
  await expect(output).toContainText('已结束 · 退出码 0');
  const count=reads;
  await page.getByText('工作过程',{exact:true}).click();
  await expect(output).toHaveCount(0);
  // A forward timeline response proves a poll interval passed while hidden.
  await page.waitForResponse(response=>response.url().includes('/events?after='));
  expect(reads).toBe(count);
});
