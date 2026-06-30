package handlers

import (
	"context"
	"testing"

	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

// fakeHandler lets the tests drive HandleCommand's wrapper behaviour (panic
// recovery + timeout) without standing up real services.
type fakeHandler struct {
	fn func(ctx context.Context, cmd *client.Command) *client.CommandResponse
}

func (f *fakeHandler) Handle(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	return f.fn(ctx, cmd)
}

func newTestManager(t *testing.T, cmdType client.CommandType, h CommandHandlerInterface) *CommandManager {
	t.Helper()
	if err := logger.Init(logger.Config{Level: "error", Format: "text", Module: "test"}); err != nil {
		t.Fatalf("logger init: %v", err)
	}
	reg := &CommandRegistry{handlers: make(map[client.CommandType]CommandHandlerInterface)}
	reg.Register(cmdType, h)
	return &CommandManager{registry: reg, logger: logger.NewLogger("test")}
}

// A panicking handler must NOT unwind past HandleCommand — before the recover was
// added, a single bad command killed the command-stream goroutine and forced a full
// reconnect + re-registration. It must come back as a failure response instead.
func TestHandleCommandRecoversPanic(t *testing.T) {
	m := newTestManager(t, client.CommandType_NETWORK, &fakeHandler{
		fn: func(context.Context, *client.Command) *client.CommandResponse { panic("boom") },
	})

	resp := m.HandleCommand(context.Background(), &client.Command{Type: client.CommandType_NETWORK, CommandId: "c1"})
	if resp == nil {
		t.Fatal("panic should have been recovered into a non-nil response")
	}
	if resp.Success {
		t.Error("recovered-panic response must report failure")
	}
}

// HandleCommand must bound every command with a timeout, so the handler should see a
// context that carries a deadline.
func TestHandleCommandAppliesTimeout(t *testing.T) {
	var sawDeadline bool
	m := newTestManager(t, client.CommandType_NETWORK, &fakeHandler{
		fn: func(ctx context.Context, cmd *client.Command) *client.CommandResponse {
			_, sawDeadline = ctx.Deadline()
			return &client.CommandResponse{CommandId: cmd.CommandId, Success: true}
		},
	})

	m.HandleCommand(context.Background(), &client.Command{Type: client.CommandType_NETWORK, CommandId: "c2"})
	if !sawDeadline {
		t.Error("handler context should carry a timeout deadline")
	}
}

// An unknown command type returns a failure response, never nil.
func TestHandleCommandUnknownType(t *testing.T) {
	m := newTestManager(t, client.CommandType_NETWORK, &fakeHandler{
		fn: func(context.Context, *client.Command) *client.CommandResponse { return nil },
	})

	resp := m.HandleCommand(context.Background(), &client.Command{Type: client.CommandType_FRR, CommandId: "c3"})
	if resp == nil || resp.Success {
		t.Fatalf("unknown command type must return a failure response, got %+v", resp)
	}
}

// Download-heavy command types must get the longer budget; everything else the
// default. Critically, no type may get LESS than the default — the per-type map is
// only allowed to relax the old flat ceiling, never tighten it.
func TestCommandTimeoutFor(t *testing.T) {
	longer := []client.CommandType{
		client.CommandType_DEPLOY,
		client.CommandType_UPGRADE_LISTENER,
		client.CommandType_ENVOY_VERSION,
		client.CommandType_WAF_VERSION,
		client.CommandType_SHIELD,
	}
	for _, ct := range longer {
		if got := commandTimeoutFor(ct); got != downloadCommandTimeout {
			t.Errorf("%v should get downloadCommandTimeout (%s), got %s", ct, downloadCommandTimeout, got)
		}
	}

	other := []client.CommandType{
		client.CommandType_NETWORK,
		client.CommandType_FRR,
		client.CommandType_SERVICE,
		client.CommandType_CLIENT_STATS,
	}
	for _, ct := range other {
		if got := commandTimeoutFor(ct); got != commandTimeout {
			t.Errorf("%v should get the default (%s), got %s", ct, commandTimeout, got)
		}
		if commandTimeoutFor(ct) < commandTimeout {
			t.Errorf("%v must never get less than the old flat ceiling", ct)
		}
	}
}
