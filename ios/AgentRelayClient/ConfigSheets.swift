import SwiftUI

// MARK: - Settings Sheet Manager
//
// "Relay Configuration" lists a session's workers, skills, and MCP servers. All
// three are SESSION-SCOPED on the relay (`/api/sessions/{id}/…`), reflecting that
// session's working-directory config — so a session id is required. The JSON
// shapes mirror internal/web/commands.go (workerItem / skillItem / mcpItem).

public struct ConfigSheetsView: View {
    let hostProfile: HostProfile?
    let laptopBase: String
    let sessionId: String?
    @Environment(\.dismiss) private var dismiss
    @State private var selectedTab = 0

    public init(hostProfile: HostProfile?, laptopBase: String, sessionId: String?) {
        self.hostProfile = hostProfile
        self.laptopBase = laptopBase
        self.sessionId = sessionId
    }

    public var body: some View {
        NavigationStack {
            Group {
                if sessionId == nil {
                    ContentUnavailableView("No Session Selected",
                        systemImage: "rectangle.on.rectangle.slash",
                        description: Text("Open a session first — workers, skills, and MCP servers are configured per session."))
                } else {
                    TabView(selection: $selectedTab) {
                        WorkersListView(hostProfile: hostProfile, laptopBase: laptopBase, sessionId: sessionId)
                            .tabItem { Label("Workers", systemImage: "cpu.fill") }
                            .tag(0)

                        SkillsListView(hostProfile: hostProfile, laptopBase: laptopBase, sessionId: sessionId)
                            .tabItem { Label("Skills", systemImage: "bolt.fill") }
                            .tag(1)

                        MCPListView(hostProfile: hostProfile, laptopBase: laptopBase, sessionId: sessionId)
                            .tabItem { Label("MCP", systemImage: "network") }
                            .tag(2)
                    }
                }
            }
            .navigationTitle("Relay Configuration")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
        }
    }
}

// apiURL builds a session-scoped management URL, or nil if the host/session is missing.
private func apiURL(_ host: HostProfile?, _ laptopBase: String, _ sessionId: String?, _ suffix: String) -> URL? {
    guard let host, let sid = sessionId else { return nil }
    return URL(string: "\(host.hostUrlString)\(laptopBase)/api/sessions/\(sid)\(suffix)")
}

private func authed(_ url: URL, _ host: HostProfile?, method: String = "GET", body: Data? = nil) -> URLRequest {
    var request = URLRequest(url: url)
    request.httpMethod = method
    if body != nil { request.setValue("application/json", forHTTPHeaderField: "Content-Type") }
    request.httpBody = body
    if let token = host?.apiToken, !token.isEmpty {
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
    }
    return request
}

// MARK: - Workers List View

struct WorkersListView: View {
    let hostProfile: HostProfile?
    let laptopBase: String
    let sessionId: String?
    @State private var workers: [WorkerConfig] = []
    @State private var isLoading = false

    var body: some View {
        List {
            if isLoading {
                HStack { Spacer(); ProgressView("Loading workers…"); Spacer() }
            } else if workers.isEmpty {
                ContentUnavailableView("No Workers", systemImage: "person.3.fill",
                    description: Text("This session has no worker agents configured."))
            } else {
                ForEach(workers) { worker in
                    VStack(alignment: .leading, spacing: 6) {
                        HStack {
                            Text(worker.name).font(.headline)
                            Spacer()
                            readinessBadge(worker.ready)
                        }
                        HStack {
                            Text("Backend: \(worker.backend)")
                                .font(.caption2)
                                .foregroundColor(.blue)
                            Spacer()
                            if let model = worker.model, !model.isEmpty {
                                Text(model).font(.caption2).foregroundColor(.secondary)
                            }
                        }
                        Text(worker.enabled ? "Enabled" : "Disabled")
                            .font(.caption2)
                            .foregroundColor(worker.enabled ? .green : .secondary)
                    }
                    .padding(.vertical, 4)
                }
            }
        }
        .onAppear { fetchWorkers() }
        .refreshable { await fetchWorkersAsync() }
    }

    @ViewBuilder
    private func readinessBadge(_ ready: String?) -> some View {
        let r = ready ?? ""
        Text(r.isEmpty ? "—" : r)
            .font(.caption2)
            .padding(.horizontal, 6).padding(.vertical, 2)
            .background((r == "ready" ? Color.green : Color.orange).opacity(0.15))
            .foregroundColor(r == "ready" ? .green : .orange)
            .cornerRadius(4)
    }

    private func fetchWorkers() { Task { await fetchWorkersAsync() } }

    private func fetchWorkersAsync() async {
        guard let url = apiURL(hostProfile, laptopBase, sessionId, "/workers") else { return }
        await MainActor.run { isLoading = true }
        do {
            let (data, response) = try await URLSession.trusted.data(for: authed(url, hostProfile))
            guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
                await MainActor.run { isLoading = false }
                return
            }
            let decoded = try JSONDecoder().decode([WorkerConfig].self, from: data)
            await MainActor.run { self.workers = decoded; self.isLoading = false }
        } catch {
            print("Failed to fetch workers: \(error)")
            await MainActor.run { self.isLoading = false }
        }
    }
}

// MARK: - Skills List View

struct SkillsListView: View {
    let hostProfile: HostProfile?
    let laptopBase: String
    let sessionId: String?
    @State private var skills: [SkillConfig] = []
    @State private var isLoading = false

    var body: some View {
        List {
            if isLoading {
                HStack { Spacer(); ProgressView(); Spacer() }
            } else if skills.isEmpty {
                ContentUnavailableView("No Skills", systemImage: "bolt.badge.a.fill",
                    description: Text("This session has no skills available."))
            } else {
                ForEach(skills) { skill in
                    VStack(alignment: .leading, spacing: 4) {
                        HStack {
                            Label(skill.name, systemImage: "bolt.fill").font(.headline)
                            Spacer()
                            Toggle("", isOn: Binding(get: { skill.enabled }, set: { toggleSkill(skill, enabled: $0) }))
                                .labelsHidden()
                        }
                        if !skill.description.isEmpty {
                            Text(skill.description).font(.caption).foregroundColor(.secondary)
                        }
                        if let source = skill.source, !source.isEmpty {
                            Text(source).font(.caption2).foregroundColor(.secondary)
                        }
                    }
                }
            }
        }
        .onAppear { fetchSkills() }
        .refreshable { await fetchSkillsAsync() }
    }

    private func fetchSkills() { Task { await fetchSkillsAsync() } }

    private func fetchSkillsAsync() async {
        guard let url = apiURL(hostProfile, laptopBase, sessionId, "/skills") else { return }
        await MainActor.run { isLoading = true }
        do {
            let (data, response) = try await URLSession.trusted.data(for: authed(url, hostProfile))
            guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
                await MainActor.run { isLoading = false }
                return
            }
            let decoded = try JSONDecoder().decode([SkillConfig].self, from: data)
            await MainActor.run { self.skills = decoded; self.isLoading = false }
        } catch {
            print("Failed to fetch skills: \(error)")
            await MainActor.run { self.isLoading = false }
        }
    }

    // Toggle posts {name, enabled} to /skills/toggle (see handleSkillToggle).
    private func toggleSkill(_ skill: SkillConfig, enabled: Bool) {
        guard let url = apiURL(hostProfile, laptopBase, sessionId, "/skills/toggle") else { return }
        let body = try? JSONEncoder().encode(["name": AnyEncodableValue(skill.name), "enabled": AnyEncodableValue(enabled)])
        Task {
            _ = try? await URLSession.trusted.data(for: authed(url, hostProfile, method: "POST", body: body))
            await fetchSkillsAsync()
        }
    }
}

// MARK: - MCP List View

struct MCPListView: View {
    let hostProfile: HostProfile?
    let laptopBase: String
    let sessionId: String?
    @State private var mcpServers: [MCPServer] = []
    @State private var isLoading = false

    var body: some View {
        List {
            Section(header: Text("Connected Server Extensions")) {
                if isLoading {
                    HStack { Spacer(); ProgressView(); Spacer() }
                } else if mcpServers.isEmpty {
                    Text("No MCP servers configured for this session")
                        .font(.caption).foregroundColor(.secondary)
                } else {
                    ForEach(mcpServers) { server in
                        VStack(alignment: .leading, spacing: 4) {
                            HStack {
                                Text(server.name).fontWeight(.bold)
                                Spacer()
                                statusBadge(server)
                            }
                            Text(server.target)
                                .font(.system(.caption2, design: .monospaced))
                                .foregroundColor(.secondary)
                            if let err = server.error, !err.isEmpty {
                                Text(err).font(.caption2).foregroundColor(.red)
                            }
                        }
                    }
                    .onDelete(perform: deleteMCPServer)
                }
            }
        }
        .onAppear { fetchMCPServers() }
        .refreshable { await fetchMCPServersAsync() }
    }

    @ViewBuilder
    private func statusBadge(_ server: MCPServer) -> some View {
        let connected = server.connected ?? false
        let label = !server.enabled ? "OFF" : (connected ? "CONNECTED" : "OFFLINE")
        let color: Color = !server.enabled ? .secondary : (connected ? .green : .orange)
        Text(label)
            .font(.caption2)
            .padding(.horizontal, 6).padding(.vertical, 2)
            .background(color.opacity(0.15))
            .foregroundColor(color)
            .cornerRadius(4)
    }

    private func fetchMCPServers() { Task { await fetchMCPServersAsync() } }

    private func fetchMCPServersAsync() async {
        guard let url = apiURL(hostProfile, laptopBase, sessionId, "/mcps") else { return }
        await MainActor.run { isLoading = true }
        do {
            let (data, response) = try await URLSession.trusted.data(for: authed(url, hostProfile))
            guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
                await MainActor.run { isLoading = false }
                return
            }
            let decoded = try JSONDecoder().decode([MCPServer].self, from: data)
            await MainActor.run { self.mcpServers = decoded; self.isLoading = false }
        } catch {
            print("Failed to fetch MCP servers: \(error)")
            await MainActor.run { self.isLoading = false }
        }
    }

    // Remove posts {name} to /mcps/remove (see handleMcpRemove).
    private func deleteMCPServer(at offsets: IndexSet) {
        guard let url = apiURL(hostProfile, laptopBase, sessionId, "/mcps/remove") else { return }
        let names = offsets.map { mcpServers[$0].name }
        Task {
            for name in names {
                let body = try? JSONEncoder().encode(["name": name])
                _ = try? await URLSession.trusted.data(for: authed(url, hostProfile, method: "POST", body: body))
            }
            await fetchMCPServersAsync()
        }
    }
}

// MARK: - Local Configuration Struct Models (mirror internal/web/commands.go)

struct WorkerConfig: Codable, Identifiable {
    var id: String { name }
    let name: String
    let backend: String
    let model: String?
    let enabled: Bool
    let ready: String?
    let custom: Bool?
    let install: String?
}

struct SkillConfig: Codable, Identifiable {
    var id: String { name }
    let name: String
    let description: String
    let source: String?
    let enabled: Bool
}

struct MCPServer: Codable, Identifiable {
    var id: String { name }
    let name: String
    let target: String
    let enabled: Bool
    let connected: Bool?
    let tools: Int?
    let error: String?
}

// AnyEncodableValue lets us build a heterogeneous JSON object ({string, bool}) for
// the toggle body without a bespoke struct.
struct AnyEncodableValue: Encodable {
    let encodeImpl: (Encoder) throws -> Void
    init(_ value: String) { encodeImpl = { var c = $0.singleValueContainer(); try c.encode(value) } }
    init(_ value: Bool) { encodeImpl = { var c = $0.singleValueContainer(); try c.encode(value) } }
    func encode(to encoder: Encoder) throws { try encodeImpl(encoder) }
}
