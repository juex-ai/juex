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
	Reply AuthorizeUsage(1: string actorID, 2: string tenantID, 3: string ownerID)
	Reply RecordNotification(1: string eventJSON)
	Reply ApplicationAuthority(1: string accessJSON, 2: bool execute)
	Reply AuthorizeFleet(1: string actorID, 2: string tenantID, 3: string ownerID, 4: bool execute)
  Reply Authorize(1: Actor actor, 2: bool execute)
  Reply Peers(1: string scopeJSON)
  Reply Snapshot(1: string scopeJSON)
  Reply ModelProfile(1: string scopeJSON, 2: string configJSON)
}

service Memory {
  Reply Purge(1: string requestJSON)
	Reply Recall(1: string accessJSON, 2: string query)
	Reply Maintain(1: string scopeJSON, 2: string threadID, 3: string reason, 4: string commandID)
	Reply Contribute(1: string scopeJSON, 2: string contributionJSON)
	Reply Reviews(1: string accessJSON, 2: i32 offset, 3: i32 limit)
	Reply StorageRules(1: string accessJSON, 2: i32 offset, 3: i32 limit)
  Reply Health()
  Reply Status(1: string accessJSON)
  Reply Configure(1: string accessJSON, 2: i64 version, 3: bool enabled, 4: string strategy)
  Reply Search(1: string accessJSON, 2: string queryJSON)
  Reply Read(1: string accessJSON, 2: string requestJSON)
  Reply Facts(1: string accessJSON, 2: string queryJSON)
  Reply Domains(1: string accessJSON, 2: string requestJSON)
  Reply Propose(1: string scopeJSON, 2: string threadID, 3: string proposalJSON, 4: bool automatic, 5: string commandID)
  Reply Review(1: string scopeJSON, 2: string bindingJSON)
  Reply Decide(1: string scopeJSON, 2: string bindingJSON, 3: string decisionJSON, 4: string commandID)
  Reply CancelCommand(1: string scopeJSON, 2: string commandID)
  Reply ReviewResult(1: string accessJSON, 2: string threadID, 3: string reviewID)
  Reply Administer(1: string accessJSON, 2: string requestJSON)
}

service Calendar {
  Reply Purge(1: string requestJSON)
  Reply Health()
  Reply Status(1: string accessJSON)
  Reply Configure(1: string accessJSON, 2: i64 version, 3: bool enabled)
  Reply Schedules(1: string accessJSON, 2: i32 offset, 3: i32 limit)
  Reply Occurrences(1: string accessJSON, 2: string scheduleID, 3: i32 offset, 4: i32 limit)
  Reply Change(1: string accessJSON, 2: string scopeJSON, 3: string commandID, 4: string changeJSON)
  Reply Assignment(1: string scopeJSON, 2: string occurrenceID, 3: i64 epoch)
  Reply CancelCommand(1: string scopeJSON, 2: string commandID)
}

service Runtime {
  Reply Purge(1: string requestJSON)
	Reply Usage(1: string actorID, 2: string queryJSON)
	Reply OperatorUsage(1: string queryJSON)
	Reply ConfigureUsage(1: string policyJSON)
	Reply RecordApplicationNotice(1: string eventJSON)
	Reply AdmitApplication(1: string scopeJSON, 2: string jobJSON)
	Reply ApplicationReceipt(1: string scopeJSON, 2: string application, 3: string jobID)
	Reply CancelApplication(1: string scopeJSON, 2: string application, 3: string jobID)
  Reply Health()
  Reply Submit(1: Actor actor, 2: string requestID, 3: string threadID, 4: string text)
  Reply Threads(1: Actor actor)
  Reply Timeline(1: Actor actor, 2: string threadID, 3: i64 after, 4: i32 limit)
  Reply Compact(1: Actor actor, 2: string threadID, 3: string requestID, 4: string focus)
  Reply Archive(1: Actor actor, 2: string threadID, 3: bool archived)
  Reply Cancel(1: Actor actor, 2: string threadID)
  Reply CreateWorker(1: Actor actor, 2: string parentID, 3: string requestID, 4: string name)
}

service Execution {
  Reply Purge(1: string requestJSON)
  Reply Health()
  Reply CancelPreparedOperation(1: Actor actor, 2: string environmentID, 3: string requestID)
  Reply CancelPreparedTransfer(1: Actor actor, 2: string requestID)
  Reply BeginTransfer(1: Actor actor, 2: string requestJSON)
  Reply BeginTransferFenced(1: Actor actor, 2: string requestJSON, 3: string fenceJSON)
  Reply Transfer(1: Actor actor, 2: string transferID)
  Reply ListTransfers(1: Actor actor, 2: string after, 3: i32 limit)
  Reply CancelTransfer(1: Actor actor, 2: string transferID)
  Reply ExtendTransfer(1: Actor actor, 2: string transferID, 3: i64 waitMillis)
  Reply BeginArtifact(1: Actor actor, 2: string requestJSON)
  Reply WriteArtifact(1: Actor actor, 2: string artifactID, 3: string chunkJSON)
  Reply CommitArtifact(1: Actor actor, 2: string artifactID)
  Reply Artifact(1: Actor actor, 2: string artifactID)
  Reply Artifacts(1: Actor actor, 2: string after, 3: i32 limit)
  Reply ReadArtifact(1: Actor actor, 2: string artifactID, 3: i64 offset, 4: i32 limit)
  Reply DeleteArtifact(1: Actor actor, 2: string artifactID)
  Reply Events(1: i32 limit)
  Reply AcknowledgeEvents(1: list<string> eventIDs)
  Reply AcknowledgeOutput(1: Actor actor, 2: string environmentID, 3: string operationID, 4: i64 cursor)
  Reply PreviewPair(1: string actorID, 2: string tenantID, 3: string pairID)
  Reply ApprovePair(1: string actorID, 2: string tenantID, 3: string pairID, 4: string grantsJSON)
  Reply Devices(1: string actorID, 2: string tenantID, 3: string ownerID)
  Reply Restrict(1: string actorID, 2: string tenantID, 3: string environmentID, 4: i64 version, 5: string grantsJSON)
  Reply Revoke(1: string actorID, 2: string tenantID, 3: string environmentID)
  Reply Environments(1: Actor actor)
  Reply DefaultEnvironment(1: Actor actor)
  Reply SetDefaultEnvironment(1: Actor actor, 2: string configurationJSON)
  Reply Submit(1: Actor actor, 2: string environmentID, 3: string requestJSON, 4: i64 waitMillis)
  Reply SubmitFenced(1: Actor actor, 2: string environmentID, 3: string requestJSON, 4: i64 waitMillis, 5: string fenceJSON)
  Reply Operation(1: Actor actor, 2: string environmentID, 3: string operationID, 4: i64 cursor, 5: i32 limit)
  Reply Cancel(1: Actor actor, 2: string environmentID, 3: string operationID)
  Reply Extend(1: Actor actor, 2: string environmentID, 3: string operationID, 4: i64 waitMillis)
}
