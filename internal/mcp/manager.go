package mcp

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"sync"

	"github.com/juex-ai/juex/internal/tools"
)

// Manager owns process-scoped MCP client connections and can expose their
// tools through any number of per-session tool registries.
type Manager struct {
	mu      sync.RWMutex
	clients map[string]*Client
	tools   map[string][]ToolDescriptor
	errors  map[string]error
	closed  bool
}

func MergeConfigs(configs []Config) Config {
	merged := map[string]ServerSpec{}
	for _, c := range configs {
		for name, spec := range c.MCPServers {
			merged[name] = spec
		}
	}
	return Config{MCPServers: merged}
}

func NewManagerLayeredSoft(ctx context.Context, configs []Config, opts ConnectOptions) (*Manager, error) {
	return newManager(ctx, MergeConfigs(configs), opts), nil
}

func newManager(ctx context.Context, cfg Config, opts ConnectOptions) *Manager {
	mgr := &Manager{
		clients: map[string]*Client{},
		tools:   map[string][]ToolDescriptor{},
		errors:  map[string]error{},
	}
	for name, spec := range cfg.MCPServers {
		if err := validateToolNameServer(name); err != nil {
			mgr.errors[name] = &ServerError{Server: name, Op: "tool name", Err: err}
			continue
		}
		client, err := ConnectWithOptions(ctx, name, spec, opts)
		if err != nil {
			serverErr := &ServerError{Server: name, Op: "connect", Err: err}
			mgr.errors[name] = serverErr
			continue
		}
		mgr.clients[name] = client
		descs, err := client.ListTools(ctx)
		if err != nil {
			client.Close()
			delete(mgr.clients, name)
			serverErr := &ServerError{Server: name, Op: "tools/list", Err: err}
			mgr.errors[name] = serverErr
			continue
		}
		mgr.tools[name] = append([]ToolDescriptor(nil), descs...)
	}
	return mgr
}

func (m *Manager) RegisterTools(reg *tools.Registry) error {
	if m == nil || reg == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return fmt.Errorf("mcp: manager closed")
	}
	for serverName, descs := range m.tools {
		client := m.clients[serverName]
		for _, d := range descs {
			if err := validateToolNameParts(serverName, d.Name); err != nil {
				return &ServerError{Server: serverName, Op: "tool name", Err: err}
			}
			toolName := ToolName(serverName, d.Name)
			cli := client
			descName := d.Name
			if err := reg.Register(toolDefinition(toolName, d).Bind(func(ctx context.Context, in map[string]any) (string, error) {
				return cli.CallTool(ctx, descName, in)
			})); err != nil {
				return &ServerError{Server: serverName, Op: "register tool " + toolName, Err: err}
			}
		}
	}
	return nil
}

func (m *Manager) ToolCounts() map[string]int {
	out := map[string]int{}
	if m == nil {
		return out
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for serverName, descs := range m.tools {
		out[serverName] = len(descs)
	}
	return out
}

// ToolDescriptors returns a deterministic defensive snapshot of the tools
// discovered for each connected MCP server. Map membership is preserved for
// connected servers that advertised zero tools.
func (m *Manager) ToolDescriptors() map[string][]ToolDescriptor {
	out := map[string][]ToolDescriptor{}
	if m == nil {
		return out
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return out
	}
	for serverName, descriptors := range m.tools {
		copied := make([]ToolDescriptor, len(descriptors))
		for i, descriptor := range descriptors {
			copied[i] = descriptor
			copied[i].InputSchema = cloneJSONMap(descriptor.InputSchema)
		}
		sort.Slice(copied, func(i, j int) bool { return copied[i].Name < copied[j].Name })
		out[serverName] = copied
	}
	return out
}

func cloneJSONMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	return cloneJSONValue(value).(map[string]any)
}

func cloneJSONValue(value any) any {
	if value == nil {
		return nil
	}
	return cloneJSONReflectValue(reflect.ValueOf(value)).Interface()
}

func cloneJSONReflectValue(value reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.New(value.Type()).Elem()
		cloned.Set(cloneJSONReflectValue(value.Elem()))
		return cloned
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			cloned.SetMapIndex(iter.Key(), cloneJSONReflectValue(iter.Value()))
		}
		return cloned
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := 0; i < value.Len(); i++ {
			cloned.Index(i).Set(cloneJSONReflectValue(value.Index(i)))
		}
		return cloned
	case reflect.Array:
		cloned := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			cloned.Index(i).Set(cloneJSONReflectValue(value.Index(i)))
		}
		return cloned
	default:
		return value
	}
}

func (m *Manager) StartupErrors() map[string]string {
	out := map[string]string{}
	if m == nil {
		return out
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for serverName, err := range m.errors {
		if err != nil {
			out[serverName] = err.Error()
		}
	}
	return out
}

func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	clients := make([]*Client, 0, len(m.clients))
	for _, client := range m.clients {
		clients = append(clients, client)
	}
	m.clients = nil
	m.tools = nil
	m.errors = nil
	m.mu.Unlock()

	var firstErr error
	for _, client := range clients {
		if err := client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
