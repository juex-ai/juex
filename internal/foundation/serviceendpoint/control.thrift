// Regenerate from the repository root:
// go run github.com/cloudwego/kitex/tool/cmd/kitex@v0.16.3 -module github.com/juex-ai/juex -gen-path internal/foundation/serviceendpoint/wire internal/foundation/serviceendpoint/control.thrift
namespace go control

struct Identity {
  1: required string fleetID
  2: required string serviceID
  3: required string instanceID
}

service ServiceControl {
  Identity Inspect(1: Identity expected)
  void Stop(1: Identity expected)
}
