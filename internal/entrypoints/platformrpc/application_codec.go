package platformrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/juex-ai/juex/internal/foundation/application"
	appwire "github.com/juex-ai/juex/internal/foundation/application/rpc"
	transport "github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform"
	"io"
)

func appReply(value any, err error) (*platform.Reply, error) {
	return transport.Reply(value, appwire.ErrorCode(err)), nil
}
func appDecode(text string, value any) bool {
	if len(text) > 512<<10 {
		return false
	}
	decoder := json.NewDecoder(bytes.NewBufferString(text))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value) == nil && decoder.Decode(new(any)) == io.EOF
}
func applicationAccess(ctx context.Context, text string) (application.Access, bool) {
	var access application.Access
	if !appDecode(text, &access) {
		return access, false
	}
	role := transport.CallerRole(ctx)
	return access, (role == "management" && access.AgentID == "") || (role == "runtime" && access.AgentID != "")
}
