package mcp

// Bridging MCP servers to the agent's local tools.Tool interface so the model
// sees a single, unified tool list. Each MCP tool is namespaced as
// "mcp.<server>.<tool>" to avoid collisions with built-in tool names and to
// signal provenance to the model.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/tools"
)

// adapter wraps a single MCP-served tool as a tools.Tool. The original (server)
// name is what we send back over the wire; the namespaced name is what the
// agent and model see.
type adapter struct {
	client     *Client
	serverName string
	toolName   string
	descriptor ToolDescriptor
}

func (a *adapter) Name() string {
	return toolAdapterName(a.serverName, a.toolName)
}

func (a *adapter) Description() string {
	d := strings.TrimSpace(a.descriptor.Description)
	if d == "" {
		return fmt.Sprintf("Tool %q from MCP server %q.", a.toolName, a.serverName)
	}
	return fmt.Sprintf("[mcp:%s] %s", a.serverName, d)
}

func (a *adapter) Schema() json.RawMessage {
	if len(a.descriptor.InputSchema) == 0 {
		// Provide a permissive default so the model can still call the tool.
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return a.descriptor.InputSchema
}

// Mutates is true: we don't know what an arbitrary MCP server will do, so the
// safe default is to gate calls behind the same approval flow as bash.
func (a *adapter) Mutates() bool { return true }

func (a *adapter) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	text, isErr, err := a.client.CallTool(ctx, a.toolName, input)
	if err != nil {
		return "", err
	}
	if isErr {
		return "", fmt.Errorf("%s", strings.TrimSpace(text))
	}
	return text, nil
}

// Compile-time guarantee that *adapter satisfies tools.Tool.
var _ tools.Tool = (*adapter)(nil)

// resourceReader is a per-server tool that lists & reads MCP resources. We
// register one of these per connected server (when the server advertises
// resources/list) so the model can inspect server-managed content.
type resourceReader struct {
	client     *Client
	serverName string
	resources  []ResourceDescriptor
}

func (r *resourceReader) Name() string {
	return resourceReaderName(r.serverName)
}

func (r *resourceReader) Description() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Read a resource from MCP server %q. ", r.serverName)
	if len(r.resources) > 0 {
		b.WriteString("Available resources:\n")
		for _, res := range r.resources {
			label := res.Name
			if label == "" {
				label = res.URI
			}
			fmt.Fprintf(&b, "  - %s — %s\n", res.URI, label)
		}
	} else {
		b.WriteString("No resources advertised at startup; pass any URI the server accepts.")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (r *resourceReader) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"uri": {"type": "string", "description": "Resource URI to read."}
		},
		"required": ["uri"]
	}`)
}

func (r *resourceReader) Mutates() bool { return false }

func (r *resourceReader) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %v", err)
	}
	if in.URI == "" {
		return "", fmt.Errorf("uri is required")
	}
	return r.client.ReadResource(ctx, in.URI)
}

var _ tools.Tool = (*resourceReader)(nil)

// PromptDef is one server-supplied prompt template plus the connection it came
// from, suitable for surfacing as a slash command in the TUI.
type PromptDef struct {
	ServerName string           // e.g. "github"
	Prompt     PromptDescriptor // its name/desc/args
	Client     *Client          // for prompts/get
}

// SlashCommand is the canonical "/mcp.<server>.<prompt>" name used by the TUI.
func (p PromptDef) SlashCommand() string {
	return "/mcp." + p.ServerName + "." + p.Prompt.Name
}

// ServerInfo is a per-server capability summary suitable for a status/sidebar
// panel: the server's name and how many tools, resources, and prompts it
// advertised at connect time.
type ServerInfo struct {
	Name      string
	Tools     int
	Resources int
	Prompts   int
}

// ServerFailure records a server that was attempted but failed to connect or
// list its tools, with the error — so status displays can show why a server
// that's enabled isn't available.
type ServerFailure struct {
	Name string
	Err  string
}

// Manager owns the lifetime of all connected MCP clients and registers their
// tools with an agent's registry. After connecting, Prompts() lists every
// server-supplied prompt template (ready to be exposed as slash commands),
// Servers() lists the connected servers with their capability counts, and
// Failures() lists the servers that failed to start.
type Manager struct {
	conns    []connected
	prompts  []PromptDef
	servers  []ServerInfo
	failures []ServerFailure
	reg      *tools.Registry // the registry tools were registered into (for reload)
}

// connected is one live MCP server plus the capabilities it advertised.
type connected struct {
	client    *Client
	name      string
	tools     []ToolDescriptor
	resources []ResourceDescriptor
}

// toolAdapterName / resourceReaderName define the registry names for MCP tools,
// shared by the adapters and by reload's unregister step.
func toolAdapterName(server, tool string) string { return fmt.Sprintf("mcp.%s.%s", server, tool) }
func resourceReaderName(server string) string    { return fmt.Sprintf("mcp.%s.read_resource", server) }

// Prompts returns every server-supplied prompt template. Order is stable.
func (m *Manager) Prompts() []PromptDef { return m.prompts }

// CatalogTool is one connected MCP tool, with its registry-qualified name
// (mcp.<server>.<tool>) and description — for callers that want to list/choose
// MCP tools (e.g. the Agent Builder's MCP node selector).
type CatalogTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ToolCatalog returns every connected server's tools as registry-qualified
// entries, in connection order.
func (m *Manager) ToolCatalog() []CatalogTool {
	var out []CatalogTool
	for _, c := range m.conns {
		for _, td := range c.tools {
			out = append(out, CatalogTool{Name: toolAdapterName(c.name, td.Name), Description: td.Description})
		}
	}
	return out
}

// Servers returns a summary of every connected MCP server (name + capability
// counts), in connection order. Empty when no servers are configured or all
// failed to start.
func (m *Manager) Servers() []ServerInfo { return m.servers }

// Failures returns the servers that were attempted but failed to connect or
// list their tools, with the error for each.
func (m *Manager) Failures() []ServerFailure { return m.failures }

// Connected reports whether the named server connected, returning its info.
func (m *Manager) Connected(name string) (ServerInfo, bool) {
	for _, s := range m.servers {
		if s.Name == name {
			return s, true
		}
	}
	return ServerInfo{}, false
}

// Failure returns the recorded connection error for the named server, if any.
func (m *Manager) Failure(name string) (string, bool) {
	for _, f := range m.failures {
		if f.Name == name {
			return f.Err, true
		}
	}
	return "", false
}

// Register connects to every server in cfgs and registers their tools on the
// registry. It's a convenience for ConnectAll + RegisterTools, kept for callers
// that connect once at startup.
func Register(ctx context.Context, registry *tools.Registry, cfgs []Config, logger func(string)) *Manager {
	m := ConnectAll(ctx, cfgs, logger)
	m.RegisterTools(registry)
	return m
}

// ConnectAll connects to every server in cfgs and lists its tools/resources/
// prompts, but does not touch any registry. Servers that fail to start or list
// capabilities are logged and skipped — they must never block the agent.
func ConnectAll(ctx context.Context, cfgs []Config, logger func(string)) *Manager {
	if logger == nil {
		logger = func(string) {}
	}
	m := &Manager{}
	for _, cfg := range cfgs {
		client, err := Connect(ctx, cfg, logger)
		if err != nil {
			logger(fmt.Sprintf("mcp: %s connect failed: %v", cfg.Name, err))
			m.failures = append(m.failures, ServerFailure{Name: cfg.Name, Err: err.Error()})
			continue
		}

		listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		descriptors, err := client.ListTools(listCtx)
		cancel()
		if err != nil {
			logger(fmt.Sprintf("mcp: %s tools/list failed: %v", cfg.Name, err))
			_ = client.Close()
			m.failures = append(m.failures, ServerFailure{Name: cfg.Name, Err: "tools/list: " + err.Error()})
			continue
		}

		// Resources are optional — many servers don't implement them.
		listCtx, cancel = context.WithTimeout(ctx, 10*time.Second)
		resources, rerr := client.ListResources(listCtx)
		cancel()
		if rerr != nil {
			logger(fmt.Sprintf("mcp: %s resources/list failed: %v", cfg.Name, rerr))
			resources = nil
		}

		// Prompts are likewise optional; collect them so the host can expose
		// each as a slash command.
		listCtx, cancel = context.WithTimeout(ctx, 10*time.Second)
		prompts, perr := client.ListPrompts(listCtx)
		cancel()
		if perr != nil {
			logger(fmt.Sprintf("mcp: %s prompts/list failed: %v", cfg.Name, perr))
		}
		for _, p := range prompts {
			m.prompts = append(m.prompts, PromptDef{ServerName: cfg.Name, Prompt: p, Client: client})
		}

		m.conns = append(m.conns, connected{client: client, name: cfg.Name, tools: descriptors, resources: resources})
		m.servers = append(m.servers, ServerInfo{
			Name:      cfg.Name,
			Tools:     len(descriptors),
			Resources: len(resources),
			Prompts:   len(prompts),
		})
		logger(fmt.Sprintf("mcp: %s connected (%d tools, %d resources, %d prompts)",
			cfg.Name, len(descriptors), len(resources), len(prompts)))
	}
	return m
}

// RegisterTools registers every connected server's tool (and resource-reader)
// adapters on registry, remembering it so Reload can re-register there.
func (m *Manager) RegisterTools(registry *tools.Registry) {
	m.reg = registry
	for _, c := range m.conns {
		for _, td := range c.tools {
			registry.Register(&adapter{client: c.client, serverName: c.name, toolName: td.Name, descriptor: td})
		}
		if len(c.resources) > 0 {
			registry.Register(&resourceReader{client: c.client, serverName: c.name, resources: c.resources})
		}
	}
}

// detach unregisters this manager's tools from the registry it was registered
// into (no-op if it was never registered).
func (m *Manager) detach() {
	if m.reg == nil {
		return
	}
	for _, c := range m.conns {
		for _, td := range c.tools {
			m.reg.Unregister(toolAdapterName(c.name, td.Name))
		}
		if len(c.resources) > 0 {
			m.reg.Unregister(resourceReaderName(c.name))
		}
	}
}

// Reload disconnects, then reconnects to cfgs and re-registers tools on the same
// registry. Use after MCP config/enable changes so they take effect without a
// restart. Caller should ensure no turn is running.
func (m *Manager) Reload(ctx context.Context, cfgs []Config, logger func(string)) {
	reg := m.reg
	m.detach()
	m.Close()
	fresh := ConnectAll(ctx, cfgs, logger)
	m.conns = fresh.conns
	m.prompts = fresh.prompts
	m.servers = fresh.servers
	m.failures = fresh.failures
	if reg != nil {
		m.RegisterTools(reg)
	}
}

// Close shuts down every connected server. Safe to call multiple times.
func (m *Manager) Close() {
	for _, c := range m.conns {
		_ = c.client.Close()
	}
	m.conns = nil
}
