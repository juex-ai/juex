// Regenerate from the repository root with the pinned Kitex tool:
// go run github.com/cloudwego/kitex/tool/cmd/kitex@v0.16.3 -module github.com/juex-ai/juex -gen-path internal/foundation/platformrpc/wire internal/foundation/platformrpc/platform.thrift
namespace go platform

struct Actor {
  1: required string userID
  2: required string tenantID
  3: required string agentID
}

// Domain JSON is versioned with the services in this single-release deployment.
// Replies expose controlled error codes, never raw database/provider errors.
struct Reply {
  1: required string code
  2: required string json
}

service Management {
	Reply AuthorizeFleet(1: string actorID, 2: string tenantID, 3: string ownerID, 4: bool execute)
  Reply Authorize(1: Actor actor, 2: bool execute)
  Reply Snapshot(1: string scopeJSON)
  Reply ModelProfile(1: string scopeJSON, 2: string configJSON)
}

service Runtime {
  Reply Health()
  Reply Submit(1: Actor actor, 2: string requestID, 3: string threadID, 4: string text)
  Reply Threads(1: Actor actor)
  Reply Timeline(1: Actor actor, 2: string threadID, 3: i64 after, 4: i32 limit)
  Reply Cancel(1: Actor actor, 2: string threadID)
  Reply CreateWorker(1: Actor actor, 2: string parentID, 3: string requestID, 4: string name)
}

service Execution {
  Reply Health()
  Reply Events(1: i32 limit)
  Reply AcknowledgeEvents(1: list<string> eventIDs)
  Reply PreviewPair(1: string actorID, 2: string tenantID, 3: string pairID)
  Reply ApprovePair(1: string actorID, 2: string tenantID, 3: string pairID, 4: string grantsJSON)
  Reply Devices(1: string actorID, 2: string tenantID, 3: string ownerID)
  Reply Restrict(1: string actorID, 2: string tenantID, 3: string environmentID, 4: i64 version, 5: string grantsJSON)
  Reply Revoke(1: string actorID, 2: string tenantID, 3: string environmentID)
  Reply Environments(1: Actor actor)
  Reply Submit(1: Actor actor, 2: string environmentID, 3: string requestJSON, 4: i64 waitMillis)
  Reply SubmitFenced(1: Actor actor, 2: string environmentID, 3: string requestJSON, 4: i64 waitMillis, 5: string fenceJSON)
  Reply Operation(1: Actor actor, 2: string environmentID, 3: string operationID, 4: i64 cursor, 5: i32 limit)
  Reply Cancel(1: Actor actor, 2: string environmentID, 3: string operationID)
  Reply Extend(1: Actor actor, 2: string environmentID, 3: string operationID, 4: i64 waitMillis)
}
