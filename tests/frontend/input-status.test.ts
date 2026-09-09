import assert from "node:assert/strict";
import test from "node:test";
import { createLiveInputStatus, inputStatusLabel, mergeInputStatus, projectInputStatusEvent } from "../../frontend/src/modules/input-tracking/state.ts";
import { mergeOlderThreadPage } from "../../frontend/src/lib/thread-messages.ts";
import type { BrowserEvent, InputStatus, ThreadShowResponse } from "../../frontend/src/types.ts";

const first: InputStatus = { input_id:"input-first",message_id:"msg-first",scope_id:"g000001" };
const second: InputStatus = { input_id:"input-second",message_id:"msg-second",scope_id:"g000001" };
const checked = { input_ids:[first.input_id], scope_id:first.scope_id, checked_at:"2026-09-09T01:00:00Z", message_id:"assistant-answer",tool_use_id:"check-call" };
const event = (type: string, payload: unknown) => ({type,payload} as BrowserEvent);

test("check before history survives pagination, replay overlap and scope ending without checking unrelated inputs",()=>{
 let live=projectInputStatusEvent(createLiveInputStatus(),event("input.checked",checked));
 live=projectInputStatusEvent(live,event("input.tracked",first));
 live=projectInputStatusEvent(live,event("input.checked",{...checked,checked_at:"later"}));
 const current={input_tracking:{scope_id:first.scope_id,messages:{[second.message_id]:second}},messages:[]} as unknown as ThreadShowResponse;
 const older={input_tracking:{scope_id:first.scope_id,messages:{[first.message_id]:first}},messages:[]} as unknown as ThreadShowResponse;
 const page=mergeInputStatus(mergeOlderThreadPage(current,older).input_tracking,live)!;
 assert.equal(page.messages[first.message_id].checked_at,checked.checked_at);
 assert.equal(page.messages[second.message_id].checked_at,undefined);
 assert.equal(inputStatusLabel(page.messages[first.message_id],"g000002"),"Checked by Agent");
 assert.equal(inputStatusLabel(second,"g000002"),"Tracking ended");
 assert.equal(inputStatusLabel(second,second.scope_id),"Not checked");
 assert.equal(mergeInputStatus(undefined,live),undefined);
});

test("ordinary completion, failures and checks from another scope do not mark an input",()=>{
 let live=createLiveInputStatus();
 for(const type of ["turn.completed","turn.errored","context.compact.completed","tool.completed"]) {
  live=projectInputStatusEvent(live,event(type,{}));
 }
 live=projectInputStatusEvent(live,event("input.checked",{...checked,scope_id:"other"}));
 const page=mergeInputStatus({scope_id:first.scope_id,messages:{[first.message_id]:first}},live)!;
 assert.equal(page.messages[first.message_id].checked_at,undefined);
 assert.equal(Object.keys(mergeInputStatus({scope_id:first.scope_id,messages:{}},live)!.messages).length,0);
});

test("status read failure does not pretend an active scope ended",()=>{
 const live=projectInputStatusEvent(createLiveInputStatus(),event("input.tracked",first));
 assert.equal(mergeInputStatus({scope_id:"",messages:{},error:"unavailable"},live),undefined);
});

test("scope invalidation requests a fresh baseline instead of guessing from replayed input IDs",async()=>{
 const {createLiveThreadProjection,projectLiveThreadEvent}=await import("../../frontend/src/lib/live-thread-projection.ts");
 const result=projectLiveThreadEvent(createLiveThreadProjection(),event("input.scope_changed",{scope_id:"g000002"}));
 assert.deepEqual(result.effects,[{type:"refresh",preserveLiveMessages:true,preserveLoadedHistory:true}]);
});
