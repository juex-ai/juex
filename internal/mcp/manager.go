package mcp

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"sync"

	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/tools"
)

const ModuleID runtimemodule.ID = "mcp"

type Module struct {
	manager     *Manager
	descriptors map[string][]ToolDescriptor
}

func NewModule(manager *Manager) *Module { return &Module{manager: manager} }

// NewDescriptorModule builds the same Tool catalog without live clients. It is
// used by read-only status projections that already have discovered descriptors.
func NewDescriptorModule(descriptors map[string][]ToolDescriptor) *Module {
	cloned := make(map[string][]ToolDescriptor, len(descriptors))
	for serverName, tools := range descriptors {
		cloned[serverName] = append([]ToolDescriptor(nil), tools...)
	}
	return &Module{descriptors: cloned}
}

func (*Module) ID() runtimemodule.ID { return ModuleID }

func (m *Module) Tools(context.Context, runtimemodule.ToolContext) ([]tools.Tool, error) {
	if m == nil || m.manager == nil {
		if m == nil {
			return nil, nil
		}
		return descriptorTools(m.descriptors)
	}
	return m.manager.Tools()
}

func descriptorTools(descriptors map[string][]ToolDescriptor) ([]tools.Tool, error) {
	serverNames := make([]string, 0, len(descriptors))
	for serverName := range descriptors {
		serverNames = append(serverNames, serverName)
	}
	sort.Strings(serverNames)
	var provided []tools.Tool
	for _, serverName := range serverNames {
		if err := validateToolNameServer(serverName); err != nil {
			return nil, &ServerError{Server: serverName, Op: "tool name", Err: err}
		}
		for _, descriptor := range descriptors[serverName] {
			if err := validateToolNameParts(serverName, descriptor.Name); err != nil {
				return nil, &ServerError{Server: serverName, Op: "tool name", Err: err}
			}
			toolName := ToolName(serverName, descriptor.Name)
			provided = append(provided, toolDefinition(toolName, descriptor).Bind(func(context.Context, map[string]any) (string, error) {
				return "", fmt.Errorf("mcp: descriptor-only tool %q is not executable", toolName)
			}))
		}
	}
	return provided, nil
}

// Manager owns process-scoped MCP client connections and can expose their
// tools through any number of per-session tool registries.
type Manager struct {
	mu      sync.RWMutex
	clients map[string]*Client
	tools   map[string][]ToolDescriptor
	errors  map[string]error
	specs   map[string]ServerSpec
	sources map[string]string
	closed  bool
}

type RuntimeConnectionSpec struct {
	Source string
	Spec   ServerSpec
}

func MergeConfigs(configs []Config) Config {
	merged := map[string]ServerSpec{}
	sources := map[string]string{}
	for _, c := range configs {
		for name, spec := range c.MCPServers {
			merged[name] = spec
			if source := c.Sources[name]; source != "" {
				sources[name] = source
			} else {
				delete(sources, name)
			}
		}
	}
	return Config{MCPServers: merged, Sources: sources}
}

func NewManagerLayeredSoft(ctx context.Context, configs []Config, opts ConnectOptions) (*Manager, error) {
	return newManager(ctx, MergeConfigs(configs), opts), nil
}

// NewManagerStrict connects every configured server and discovers all Tools.
// Any failure closes already connected servers and returns no Manager.
func NewManagerStrict(ctx context.Context, cfg Config, opts ConnectOptions) (*Manager, error) {
	mgr := &Manager{
		clients: map[string]*Client{},
		tools:   map[string][]ToolDescriptor{},
		errors:  map[string]error{},
		specs:   map[string]ServerSpec{},
		sources: map[string]string{},
	}
	serverNames := make([]string, 0, len(cfg.MCPServers))
	for name := range cfg.MCPServers {
		serverNames = append(serverNames, name)
	}
	sort.Strings(serverNames)
	for _, name := range serverNames {
		spec := cfg.MCPServers[name]
		mgr.sources[name] = cfg.Sources[name]
		mgr.specs[name] = spec
		if err := validateToolNameServer(name); err != nil {
			_ = mgr.Close()
			return nil, &ServerError{Server: name, Op: "tool name", Err: err}
		}
		client, err := ConnectWithOptions(ctx, name, spec, opts)
		if err != nil {
			_ = mgr.Close()
			return nil, &ServerError{Server: name, Op: "connect", Err: err}
		}
		mgr.clients[name] = client
		descriptors, err := client.ListTools(ctx)
		if err != nil {
			_ = mgr.Close()
			return nil, &ServerError{Server: name, Op: "tools/list", Err: err}
		}
		for _, descriptor := range descriptors {
			if err := validateToolNameParts(name, descriptor.Name); err != nil {
				_ = mgr.Close()
				return nil, &ServerError{Server: name, Op: "tool name", Err: err}
			}
		}
		mgr.tools[name] = append([]ToolDescriptor(nil), descriptors...)
	}
	return mgr, nil
}

func newManager(ctx context.Context, cfg Config, opts ConnectOptions) *Manager {
	mgr := &Manager{
		clients: map[string]*Client{},
		tools:   map[string][]ToolDescriptor{},
		errors:  map[string]error{},
		specs:   map[string]ServerSpec{},
		sources: map[string]string{},
	}
	for name, spec := range cfg.MCPServers {
		mgr.sources[name] = cfg.Sources[name]
		mgr.specs[name] = ServerSpec{
			Type:    spec.Type,
			Command: spec.Command,
			Args:    append([]string(nil), spec.Args...),
			URL:     spec.URL,
		}
		if err := validateToolNameServer(name); err != nil {
			mgr.errors[name] = &ServerError{Server: name, Op: "tool name", Err: err}
			continue
		}
		if spec.URL != "" {
			if result := CheckRemoteSelection(name, spec); result.Status != ReadinessStatusOK {
				mgr.errors[name] = &ServerError{
					Server: name,
					Op:     "readiness " + string(result.Stage),
					Err:    result.Err,
				}
				continue
			}
			if result := CheckRemoteCredentials(name, spec); result.Status != ReadinessStatusOK {
				mgr.errors[name] = &ServerError{
					Server: name,
					Op:     "readiness " + string(result.Stage),
					Err:    result.Err,
				}
				continue
			}
		}
		client, err := ConnectWithOptions(ctx, name, spec, opts)
		if err != nil {
			mgr.errors[name] = remoteReadinessServerError(name, spec, "connect", err)
			continue
		}
		mgr.clients[name] = client
		descs, err := client.ListTools(ctx)
		if err != nil {
			client.Close()
			delete(mgr.clients, name)
			mgr.errors[name] = remoteReadinessServerError(name, spec, "tools/list", err)
			continue
		}
		mgr.tools[name] = append([]ToolDescriptor(nil), descs...)
	}
	return mgr
}

func (m *Manager) Tools() ([]tools.Tool, error) {
	if m == nil {
		return nil, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return nil, fmt.Errorf("mcp: manager closed")
	}
	var provided []tools.Tool
	serverNames := make([]string, 0, len(m.tools))
	for serverName := range m.tools {
		serverNames = append(serverNames, serverName)
	}
	sort.Strings(serverNames)
	for _, serverName := range serverNames {
		descs := m.tools[serverName]
		client := m.clients[serverName]
		for _, d := range descs {
			if err := validateToolNameParts(serverName, d.Name); err != nil {
				return nil, &ServerError{Server: serverName, Op: "tool name", Err: err}
			}
			toolName := ToolName(serverName, d.Name)
			cli := client
			descName := d.Name
			provided = append(provided, toolDefinition(toolName, d).Bind(func(ctx context.Context, in map[string]any) (string, error) {
				return cli.CallTool(ctx, descName, in)
			}))
		}
	}
	return provided, nil
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

// RuntimeConnectionSpecs returns display-safe startup transport metadata owned
// by this manager. It remains stable if configuration files change in place.
func (m *Manager) RuntimeConnectionSpecs() map[string]RuntimeConnectionSpec {
	out := map[string]RuntimeConnectionSpec{}
	if m == nil {
		return out
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return out
	}
	for name, spec := range m.specs {
		spec.Args = append([]string(nil), spec.Args...)
		if displayURL, err := spec.DisplayURL(); err != nil {
			spec.URL = ""
		} else if displayURL != "" {
			spec.URL = displayURL
		}
		out[name] = RuntimeConnectionSpec{Source: m.sources[name], Spec: spec}
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
			message := err.Error()
			if spec, ok := m.specs[serverName]; ok {
				if safeMessage, safeErr := spec.DisplaySafeText(message); safeErr == nil {
					message = safeMessage
				} else {
					message = fmt.Sprintf("mcp[%s]: startup error diagnostic redacted", serverName)
				}
			}
			out[serverName] = message
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
	m.specs = nil
	m.mu.Unlock()

	var firstErr error
	for _, client := range clients {
		if err := client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
