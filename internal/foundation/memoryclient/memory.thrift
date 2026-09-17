// Regenerate from the repository root:
// go run github.com/cloudwego/kitex/tool/cmd/kitex@v0.16.3 -module github.com/juex-ai/juex -gen-path internal/foundation/memoryclient/wire internal/foundation/memoryclient/memory.thrift
namespace go memorywire

// Business payload schemas are the typed Go contract in memoryclient/types.go.
// The envelope binds every method to the expected service instance and a frozen caller.
struct Call {
  1: required string fleetID
  2: required string serviceID
  3: required string instanceID
  4: required string caller
  5: required string payload
}
service Memory {
  string Status(1: Call request)
  string Search(1: Call request)
  string Read(1: Call request)
  string Propose(1: Call request)
  string Result(1: Call request)
  string Claim(1: Call request)
  string Decide(1: Call request)
  string Fail(1: Call request)
  string Revoke(1: Call request)
  string Participation(1: Call request)
  string Contribute(1: Call request)
  string Maintain(1: Call request)
  string History(1: Call request)
  string Recall(1: Call request)
  string Admin(1: Call request)
}
