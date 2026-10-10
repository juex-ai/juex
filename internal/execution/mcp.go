package execution

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

type MCPTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type MCPToolList struct {
	ID            string    `json:"id"`
	State         string    `json:"state"`
	RequestedAt   time.Time `json:"requested_at"`
	Cursor        string    `json:"cursor"`
	NextCursor    string    `json:"next_cursor"`
	Tools         []MCPTool `json:"tools"`
	OutputExpired bool      `json:"output_expired"`
	Incomplete    bool      `json:"incomplete"`
}

type MCPStatus struct {
	ID            string                      `json:"id"`
	EnvironmentID string                      `json:"environment_id"`
	Environment   string                      `json:"environment"`
	State         string                      `json:"state"`
	ObservedState string                      `json:"observed_state"`
	CreatedAt     time.Time                   `json:"created_at"`
	CanRefresh    bool                        `json:"can_refresh"`
	BindingID     string                      `json:"binding_id"`
	Directory     string                      `json:"directory"`
	Handshake     *execprotocol.MCPConnection `json:"handshake"`
	LatestTools   *MCPToolList                `json:"latest_tools"`
}

type MCPPage struct {
	Items      []MCPStatus `json:"items"`
	Next       string      `json:"next"`
	ObservedAt time.Time   `json:"observed_at"`
}

// MCPRecord stays behind the service projection; requests may contain secrets.
type MCPRecord struct {
	Operation Operation
	Device    Device
	Latest    *Operation
}

type MCPCursor struct {
	Time        time.Time
	Environment string
	ID          string
}

type MCPInspectionStore interface {
	InspectMCP(context.Context, Scope, MCPCursor) ([]MCPRecord, time.Time, error)
}

func (s *Service) InspectMCP(ctx context.Context, actor, tenant, agent, after string) (MCPPage, error) {
	var cursor MCPCursor
	if after != "" {
		if len(after) > 1024 {
			return MCPPage{}, execprotocol.ErrInvalid
		}
		data, err := base64.RawURLEncoding.DecodeString(after)
		if len(after) > 1024 || err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Time.IsZero() || cursor.Environment == "" || cursor.ID == "" {
			return MCPPage{}, execprotocol.ErrInvalid
		}
	}
	scope, err := s.Authority.Agent(ctx, actor, tenant, agent, false)
	if err != nil {
		return MCPPage{}, err
	}
	store, ok := s.Store.(MCPInspectionStore)
	if !ok {
		return MCPPage{}, execprotocol.ErrUnavailable
	}
	records, observed, err := store.InspectMCP(ctx, scope, cursor)
	if err != nil {
		return MCPPage{}, err
	}
	page := MCPPage{Items: []MCPStatus{}, ObservedAt: observed}
	if len(records) > 50 {
		last := records[49].Operation
		data, _ := json.Marshal(MCPCursor{last.CreatedAt, last.EnvironmentID, last.ID})
		page.Next = base64.RawURLEncoding.EncodeToString(data)
		records = records[:50]
	}
	for _, record := range records {
		op, device := record.Operation, record.Device
		status := MCPStatus{ID: op.ID, EnvironmentID: op.EnvironmentID, Environment: device.Name, State: op.State, ObservedState: op.State, CreatedAt: op.CreatedAt, Handshake: op.Snapshot.MCP}
		if !execprotocol.State(op.State).Terminal() {
			status.State = "unconfirmed"
			if device.Online && scope.SameAuthority(op.Scope) && permits(device, scope, op.Request) && !op.CancelRequested && (op.Request.AuthorizationVersion == 0 || op.Request.AuthorizationVersion == device.Version) {
				status.State = op.State
				status.CanRefresh = op.State == "running" && op.Snapshot.MCP != nil
				if op.State == "running" && op.Snapshot.MCP == nil {
					status.State = "connecting"
				}
			}
		}
		var args struct {
			Extension        *execprotocol.ExtensionContext `json:"extension"`
			WorkingDirectory string                         `json:"working_directory"`
		}
		if json.Unmarshal(op.Request.Arguments, &args) == nil {
			status.Directory = args.WorkingDirectory
			if args.Extension != nil {
				status.BindingID = args.Extension.BindingID
				status.Directory = args.Extension.Directory
			}
		}
		if record.Latest != nil {
			status.LatestTools = projectMCPTools(*record.Latest)
		}
		page.Items = append(page.Items, status)
	}
	return page, nil
}

func projectMCPTools(op Operation) *MCPToolList {
	var args struct {
		Cursor string `json:"cursor"`
	}
	_ = json.Unmarshal(op.Request.Arguments, &args)
	result := &MCPToolList{ID: op.ID, State: op.State, RequestedAt: op.CreatedAt, Cursor: args.Cursor, Tools: []MCPTool{}, OutputExpired: op.Snapshot.OutputExpired}
	if op.State != "completed" || result.OutputExpired {
		return result
	}
	var list struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
		NextCursor string `json:"nextCursor"`
	}
	if op.Snapshot.Truncated || op.ResultCursor > int64(len(op.Snapshot.Output)) || json.Unmarshal(op.Snapshot.Output, &list) != nil {
		result.Incomplete = true
		return result
	}
	result.NextCursor = list.NextCursor
	for _, tool := range list.Tools {
		result.Tools = append(result.Tools, MCPTool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
	}
	return result
}

type MCPRefresh struct {
	ID     string `json:"id"`
	Cursor string `json:"cursor"`
}

func (s *Service) RefreshMCPTools(ctx context.Context, actor, tenant, agent, environment, connection string, change MCPRefresh) (*MCPToolList, error) {
	if len(change.Cursor) > 4096 || !utf8.ValidString(change.Cursor) {
		return nil, execprotocol.ErrInvalid
	}
	op, err := s.Operation(ctx, actor, tenant, agent, environment, connection, 0, 1)
	if err != nil {
		return nil, err
	}
	if op.Request.Kind != "mcp_connect" || op.State != "running" || op.Snapshot.MCP == nil || op.CancelRequested {
		return nil, execprotocol.ErrConflict
	}
	device, err := s.Store.Device(ctx, environment)
	if err != nil {
		return nil, err
	}
	if !device.Online {
		return nil, execprotocol.ErrUnavailable
	}
	if op.Request.AuthorizationVersion != 0 && op.Request.AuthorizationVersion != device.Version {
		return nil, execprotocol.ErrDenied
	}
	args, _ := json.Marshal(map[string]string{"connection_id": connection, "cursor": change.Cursor})
	request := execprotocol.Request{Version: execprotocol.Version, ID: change.ID, AgentID: agent, Kind: "mcp_list", Arguments: args, AuthorizationVersion: device.Version}
	result, err := s.Submit(ctx, actor, tenant, environment, request, time.Minute)
	if err != nil {
		return nil, err
	}
	result, err = s.Store.Operation(ctx, environment, result.ID, 0, 256<<10)
	if err != nil {
		return nil, err
	}
	return projectMCPTools(result), nil
}
