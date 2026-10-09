package platformrpc

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/cloudwego/kitex/pkg/remote"
	transheader "github.com/cloudwego/kitex/pkg/remote/transmeta"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/cloudwego/kitex/pkg/transmeta"
	"github.com/cloudwego/kitex/transport"
)

func TestDeadlineMetadataBudget(t *testing.T) {
	for _, tc := range []struct {
		name      string
		parent    time.Duration
		cancelled bool
		wantError error
		maxMillis int64
	}{
		{name: "default", maxMillis: 10000},
		{name: "long_parent", parent: time.Minute, maxMillis: 10000},
		{name: "short_parent", parent: time.Second, maxMillis: 1000},
		{name: "expired", parent: -time.Second, wantError: context.DeadlineExceeded},
		{name: "sub_millisecond", parent: 500 * time.Microsecond, wantError: context.DeadlineExceeded},
		{name: "cancelled", cancelled: true, wantError: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.parent != 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.parent)
				defer cancel()
			}
			if tc.cancelled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			config := rpcinfo.NewRPCConfig()
			mutable := rpcinfo.AsMutableRPCConfig(config)
			if err := mutable.SetRPCTimeout(10 * time.Second); err != nil {
				t.Fatal(err)
			}
			mutable.LockConfig(rpcinfo.BitRPCTimeout)
			if err := mutable.SetTransportProtocol(transport.TTHeader); err != nil {
				t.Fatal(err)
			}
			info := rpcinfo.NewRPCInfo(rpcinfo.NewEndpointInfo("runtime", "Events", nil, nil), rpcinfo.NewEndpointInfo("management", "Authorize", nil, nil), rpcinfo.NewInvocation("management", "Authorize"), config, rpcinfo.NewRPCStats())
			message := remote.NewMessage(nil, info, remote.Call, remote.Client)
			_, err := (deadlineMetadata{transmeta.ClientTTHeaderHandler}).WriteMeta(ctx, message)
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("metadata error = %v, want %v", err, tc.wantError)
			}
			if err != nil {
				return
			}
			headers := message.TransInfo().TransIntInfo()
			millis, err := strconv.ParseInt(headers[transheader.RPCTimeout], 10, 64)
			if err != nil || millis <= 0 || millis > tc.maxMillis {
				t.Fatalf("RPC budget = %d ms: %v", millis, err)
			}
			if tc.parent <= 0 && millis != tc.maxMillis {
				t.Fatalf("default RPC budget = %d ms", millis)
			}
			if headers[transheader.ToService] != "management" || headers[transheader.ToMethod] != "Authorize" {
				t.Fatal("standard RPC metadata lost", headers)
			}
		})
	}
}
