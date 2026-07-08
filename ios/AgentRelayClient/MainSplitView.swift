import SwiftUI
import SwiftData

public struct MainSplitView: View {
    @Environment(\.modelContext) private var modelContext
    @Query(sort: \HostProfile.createdAt, order: .reverse) private var profiles: [HostProfile]
    
    @State private var selectedProfile: HostProfile?
    @State private var sessions: [Session] = []
    @State private var selectedSession: Session?
    
    // View state models
    @State private var viewModel = SessionViewModel()
    @State private var showAddHost = false
    @State private var showSettings = false
    @State private var showProgressDrawer = false
    @State private var showAppleConnectLogin = false
    @State private var userProfile: UserProfile? = nil
    
    // Multi-laptop Tree state
    @State private var laptops: [Laptop] = []
    @State private var selectedLaptop: Laptop? = nil
    @State private var projects: [Project] = []
    @State private var selectedProject: Project? = nil
    
    // Temp state for new host
    @State private var newHostName = ""
    @State private var newHostUrl = "http://localhost:8080"
    @State private var newHostToken = ""
    
    public var body: some View {
        NavigationSplitView {
            // MARK: - Primary Sidebar (Hosts & Sessions)
            List(selection: $selectedSession) {
                // Connection Profiles Section
                Section(header: Text("Host Connections")) {
                    if profiles.isEmpty {
                        Text("No hosts configured")
                            .font(.caption)
                            .foregroundColor(.secondary)
                    } else {
                        ForEach(profiles) { profile in
                            HStack {
                                Circle()
                                    .fill(selectedProfile?.id == profile.id ? Color.green : Color.gray)
                                    .frame(width: 8, height: 8)
                                
                                VStack(alignment: .leading) {
                                    Text(profile.name)
                                        .fontWeight(.semibold)
                                    Text(profile.hostUrlString)
                                        .font(.caption2)
                                        .foregroundColor(.secondary)
                                }
                                Spacer()
                                if profile.isDefault {
                                    Image(systemName: "pin.fill")
                                        .font(.caption2)
                                        .foregroundColor(.secondary)
                                }
                            }
                            .contentShape(Rectangle())
                            .onTapGesture {
                                selectProfile(profile)
                            }
                        }
                        .onDelete(perform: deleteProfiles)
                    }
                    
                    Button {
                        showAddHost = true
                    } label: {
                        Label("Add Host Relay", systemImage: "plus.circle")
                    }
                }
                
                // AppleConnect Authentication Section
                if selectedProfile != nil {
                    Section(header: Text("Authentication")) {
                        if let user = userProfile {
                            HStack(spacing: 12) {
                                Image(systemName: "person.crop.circle.badge.checkmark")
                                    .font(.title2)
                                    .foregroundColor(.green)
                                
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(user.displayName)
                                        .fontWeight(.semibold)
                                    Text(user.accountname)
                                        .font(.caption2)
                                        .foregroundColor(.secondary)
                                }
                            }
                            .padding(.vertical, 4)
                        } else {
                            Button {
                                showAppleConnectLogin = true
                            } label: {
                                Label("Sign In with AppleConnect", systemImage: "key.fill")
                            }
                        }
                    }
                }
                
                // Multi-laptop Tree sections
                if let profile = selectedProfile, !laptops.isEmpty {
                    Section(header: Text("Connected Laptops")) {
                        ForEach(laptops) { laptop in
                            HStack {
                                Image(systemName: "laptopcomputer")
                                    .foregroundColor((laptop.online ?? true) ? .green : .gray)
                                Text(laptop.name)
                                    .fontWeight(selectedLaptop?.id == laptop.id ? .bold : .regular)
                                Spacer()
                                if !(laptop.online ?? true) {
                                    Text("Offline")
                                        .font(.caption2)
                                        .foregroundColor(.secondary)
                                }
                            }
                            .contentShape(Rectangle())
                            .onTapGesture {
                                selectLaptop(laptop, at: profile)
                            }
                        }
                    }
                    
                    if let laptop = selectedLaptop, !projects.isEmpty {
                        Section(header: Text("Active Projects")) {
                            ForEach(projects) { project in
                                HStack {
                                    Image(systemName: "folder.fill")
                                        .foregroundColor(.blue)
                                    Text(project.name)
                                        .fontWeight(selectedProject?.id == project.id ? .bold : .regular)
                                }
                                .contentShape(Rectangle())
                                .onTapGesture {
                                    selectProject(project, laptop: laptop, at: profile)
                                }
                            }
                        }
                    }
                }
                
                // Sessions list
                if selectedProfile != nil {
                    // Decide if we should show sessions based on mode
                    let showSessions = laptops.isEmpty || (selectedLaptop != nil && selectedProject != nil)
                    let headerTitle = laptops.isEmpty ? "Active Sessions" : "Sessions (\(selectedProject?.name ?? ""))"
                    
                    if showSessions {
                        Section(header: Text(headerTitle)) {
                            if sessions.isEmpty {
                                Text("No active sessions")
                                    .font(.caption)
                                    .foregroundColor(.secondary)
                            } else {
                                ForEach(sessions) { session in
                                    NavigationLink(value: session) {
                                        HStack {
                                            Image(systemName: "bubble.left.and.bubble.right.fill")
                                                .foregroundColor(.purple)
                                            VStack(alignment: .leading) {
                                                Text(session.title ?? "Session Details")
                                                    .fontWeight(.medium)
                                                if let status = session.status {
                                                    Text(status.capitalized)
                                                        .font(.caption2)
                                                        .foregroundColor(.secondary)
                                                }
                                            }
                                        }
                                    }
                                }
                                .onDelete(perform: deleteSessionAtHost)
                            }
                            
                            Button {
                                createNewSession()
                            } label: {
                                Label("New Session", systemImage: "square.and.pencil")
                            }
                        }
                    }
                }
            }
            .listStyle(.sidebar)
            .navigationTitle("Agent Relay")
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    EditButton()
                }
            }
            .sheet(isPresented: $showAddHost) {
                AddHostSheet()
            }
            .sheet(isPresented: $showSettings) {
                ConfigSheetsView(hostProfile: selectedProfile, laptopBase: selectedLaptop?.base ?? "", sessionId: selectedSession?.id)
            }
            .sheet(isPresented: $showAppleConnectLogin) {
                if let profile = selectedProfile, let url = URL(string: "\(profile.hostUrlString)/auth/login") {
                    AppleConnectWebView(url: url) {
                        showAppleConnectLogin = false
                        checkAuthAndFetchSessions(for: profile)
                    }
                }
            }
            
        } detail: {
            // MARK: - Detail Panel (Active Session Chat Area)
            if let session = selectedSession, let profile = selectedProfile {
                VStack(spacing: 0) {
                    // Header Bar
                    HeaderBarView(viewModel: viewModel, showProgressDrawer: $showProgressDrawer, showSettings: $showSettings)
                    
                    HStack(spacing: 0) {
                        // Main Transcript List
                        TranscriptView(viewModel: viewModel)
                            .frame(maxWidth: .infinity, maxHeight: .infinity)
                        
                        // Sliding Progress / Todo Panel for iPad / Landscape
                        if showProgressDrawer {
                            ProgressDrawer(viewModel: viewModel)
                                .frame(width: 300)
                                .transition(.move(edge: .trailing))
                        }
                    }
                    
                    // Chat Bottom Composer
                    ComposerView(viewModel: viewModel)
                }
                .navigationTitle(session.title ?? "Conversation")
                .navigationBarTitleDisplayMode(.inline)
                .onAppear {
                    viewModel.connect(host: profile, laptopBase: selectedLaptop?.base ?? "", sessionId: session.id)
                }
                .onChange(of: selectedSession) { oldValue, newValue in
                    if let newSession = newValue {
                        viewModel.connect(host: profile, laptopBase: selectedLaptop?.base ?? "", sessionId: newSession.id)
                    } else {
                        viewModel.disconnect()
                    }
                }
                .sheet(isPresented: Binding(
                    get: { viewModel.pendingMenu != nil },
                    set: { if !$0 { viewModel.dismissMenu() } }
                )) {
                    if let menu = viewModel.pendingMenu {
                        MenuPickerSheet(menu: menu, viewModel: viewModel)
                    }
                }
            } else {
                VStack(spacing: 12) {
                    Image(systemName: "cpu")
                        .resizable()
                        .scaledToFit()
                        .frame(width: 64, height: 64)
                        .foregroundColor(.secondary)
                    
                    Text("Select or Create a Session")
                        .font(.title3)
                        .fontWeight(.semibold)
                    
                    Text("Connect to a laptop/relay host to start interacting with your Community Agent.")
                        .font(.subheadline)
                        .foregroundColor(.secondary)
                        .multilineTextAlignment(.center)
                        .padding(.horizontal, 40)
                }
            }
        }
        .onAppear {
            if let first = profiles.first {
                selectProfile(first)
            }
        }
    }
    
    // MARK: - Profile Management & Actions
    
    private func selectProfile(_ profile: HostProfile) {
        selectedProfile = profile
        selectedSession = nil
        viewModel.disconnect()
        
        // Reset multi-laptop states
        laptops = []
        selectedLaptop = nil
        projects = []
        selectedProject = nil
        sessions = []
        
        checkAuthAndFetchSessions(for: profile)
    }
    
    private func checkAuthAndFetchSessions(for profile: HostProfile) {
        guard let url = URL(string: "\(profile.hostUrlString)/auth/me") else { return }
        
        var request = URLRequest(url: url)
        if !profile.apiToken.isEmpty {
            request.setValue("Bearer \(profile.apiToken)", forHTTPHeaderField: "Authorization")
        }
        
        Task {
            do {
                let (data, response) = try await URLSession.trusted.data(for: request)
                if let httpResponse = response as? HTTPURLResponse {
                    if httpResponse.statusCode == 401 {
                        // Needs AppleConnect login!
                        await MainActor.run {
                            self.userProfile = nil
                            self.showAppleConnectLogin = true
                        }
                    } else if httpResponse.statusCode == 200 {
                        // Successfully authenticated, let's decode UserProfile
                        let decodedProfile = try? JSONDecoder().decode(UserProfile.self, from: data)
                        await MainActor.run {
                            self.userProfile = decodedProfile
                            // Fetch laptops first to check for Tree Mode
                            fetchLaptops(for: profile)
                        }
                    } else {
                        fetchLaptops(for: profile)
                    }
                }
            } catch {
                print("Auth check failed, attempting to fetch laptops/sessions: \(error)")
                fetchLaptops(for: profile)
            }
        }
    }
    
    private func fetchLaptops(for profile: HostProfile) {
        guard let url = URL(string: "\(profile.hostUrlString)/api/laptops") else { return }
        
        var request = URLRequest(url: url)
        if !profile.apiToken.isEmpty {
            request.setValue("Bearer \(profile.apiToken)", forHTTPHeaderField: "Authorization")
        }
        
        Task {
            do {
                let (data, response) = try await URLSession.trusted.data(for: request)
                guard let httpResponse = response as? HTTPURLResponse else { return }
                
                if httpResponse.statusCode == 200 {
                    let decoded = try JSONDecoder().decode([Laptop].self, from: data)
                    await MainActor.run {
                        self.laptops = decoded
                        if decoded.isEmpty {
                            // Fallback to direct sessions (Mirror Mode)
                            fetchSessions(for: profile)
                        } else if let firstLaptop = decoded.first(where: { $0.online ?? true }) {
                            // Auto-select first online laptop and fetch its projects
                            selectLaptop(firstLaptop, at: profile)
                        }
                    }
                } else {
                    // Fallback to direct sessions (Mirror Mode)
                    await MainActor.run {
                        self.laptops = []
                        fetchSessions(for: profile)
                    }
                }
            } catch {
                print("Failed to fetch laptops, falling back to mirror sessions: \(error)")
                await MainActor.run {
                    self.laptops = []
                    fetchSessions(for: profile)
                }
            }
        }
    }
    
    private func selectLaptop(_ laptop: Laptop, at profile: HostProfile) {
        self.selectedLaptop = laptop
        self.selectedProject = nil
        self.projects = []
        self.sessions = []
        
        // Fetch projects for this laptop
        guard let url = URL(string: "\(profile.hostUrlString)\(laptop.base)/api/projects") else { return }
        
        var request = URLRequest(url: url)
        if !profile.apiToken.isEmpty {
            request.setValue("Bearer \(profile.apiToken)", forHTTPHeaderField: "Authorization")
        }
        
        Task {
            do {
                let (data, response) = try await URLSession.trusted.data(for: request)
                if let httpResponse = response as? HTTPURLResponse, httpResponse.statusCode == 200 {
                    let decoded = try JSONDecoder().decode([Project].self, from: data)
                    await MainActor.run {
                        self.projects = decoded
                        if let firstProject = decoded.first {
                            selectProject(firstProject, laptop: laptop, at: profile)
                        }
                    }
                }
            } catch {
                print("Failed to fetch projects: \(error)")
            }
        }
    }
    
    private func selectProject(_ project: Project, laptop: Laptop, at profile: HostProfile) {
        self.selectedProject = project
        self.sessions = []
        
        // Fetch sessions inside this project on this laptop
        guard let url = URL(string: "\(profile.hostUrlString)\(laptop.base)/api/projects/\(project.key)/sessions") else { return }
        
        var request = URLRequest(url: url)
        if !profile.apiToken.isEmpty {
            request.setValue("Bearer \(profile.apiToken)", forHTTPHeaderField: "Authorization")
        }
        
        Task {
            do {
                let (data, response) = try await URLSession.trusted.data(for: request)
                if let httpResponse = response as? HTTPURLResponse, httpResponse.statusCode == 200 {
                    let decoded = try JSONDecoder().decode([Session].self, from: data)
                    await MainActor.run {
                        self.sessions = decoded
                    }
                }
            } catch {
                print("Failed to fetch project sessions: \(error)")
            }
        }
    }
    
    private func fetchSessions(for profile: HostProfile) {
        guard let url = URL(string: "\(profile.hostUrlString)/api/sessions") else { return }
        
        var request = URLRequest(url: url)
        if !profile.apiToken.isEmpty {
            request.setValue("Bearer \(profile.apiToken)", forHTTPHeaderField: "Authorization")
        }
        
        Task {
            do {
                let (data, response) = try await URLSession.trusted.data(for: request)
                guard let httpResponse = response as? HTTPURLResponse, httpResponse.statusCode == 200 else {
                    return
                }
                let decoded = try JSONDecoder().decode([Session].self, from: data)
                await MainActor.run {
                    self.sessions = decoded
                }
            } catch {
                print("Failed to fetch sessions: \(error)")
            }
        }
    }
    
    private func createNewSession() {
        guard let profile = selectedProfile, let url = URL(string: "\(profile.hostUrlString)/api/sessions") else { return }
        
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        if !profile.apiToken.isEmpty {
            request.setValue("Bearer \(profile.apiToken)", forHTTPHeaderField: "Authorization")
        }
        
        Task {
            do {
                let (data, response) = try await URLSession.trusted.data(for: request)
                guard let httpResponse = response as? HTTPURLResponse, httpResponse.statusCode == 200 else {
                    return
                }
                let newSession = try JSONDecoder().decode(Session.self, from: data)
                await MainActor.run {
                    self.sessions.insert(newSession, at: 0)
                    self.selectedSession = newSession
                }
            } catch {
                print("Failed to create session: \(error)")
            }
        }
    }
    
    private func deleteSessionAtHost(at offsets: IndexSet) {
        guard let profile = selectedProfile else { return }
        for index in offsets {
            let session = sessions[index]
            guard let url = URL(string: "\(profile.hostUrlString)/api/sessions/\(session.id)") else { continue }
            
            var request = URLRequest(url: url)
            request.httpMethod = "DELETE"
            if !profile.apiToken.isEmpty {
                request.setValue("Bearer \(profile.apiToken)", forHTTPHeaderField: "Authorization")
            }
            
            Task {
                do {
                    let (_, response) = try await URLSession.trusted.data(for: request)
                    if let httpResponse = response as? HTTPURLResponse, httpResponse.statusCode == 200 {
                        await MainActor.run {
                            if selectedSession?.id == session.id {
                                selectedSession = nil
                            }
                            sessions.remove(at: index)
                        }
                    }
                } catch {
                    print("Failed to delete session: \(error)")
                }
            }
        }
    }
    
    private func deleteProfiles(at offsets: IndexSet) {
        for index in offsets {
            let profile = profiles[index]
            if selectedProfile?.id == profile.id {
                selectedProfile = nil
                selectedSession = nil
                viewModel.disconnect()
            }
            modelContext.delete(profile)
        }
        try? modelContext.save()
    }
    
    // MARK: - Add Host Sheets & Drawers
    
    @ViewBuilder
    private func AddHostSheet() -> some View {
        NavigationStack {
            Form {
                Section(header: Text("Details")) {
                    TextField("Profile Name (e.g. My Laptop)", text: $newHostName)
                    TextField("Relay Host URL", text: $newHostUrl)
                        .keyboardType(.URL)
                        .textInputAutocapitalization(.never)
                }
                
                Section(header: Text("Authentication (Optional)")) {
                    SecureField("Bearer API Token", text: $newHostToken)
                }
            }
            .navigationTitle("Add Host Profile")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") {
                        showAddHost = false
                    }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") {
                        let profile = HostProfile(
                            name: newHostName.isEmpty ? "Relay" : newHostName,
                            hostUrlString: newHostUrl,
                            apiToken: newHostToken
                        )
                        modelContext.insert(profile)
                        try? modelContext.save()
                        
                        // Auto-select newly added host
                        selectProfile(profile)
                        
                        // Reset temp state
                        newHostName = ""
                        newHostUrl = "http://localhost:8080"
                        newHostToken = ""
                        
                        showAddHost = false
                    }
                    .disabled(newHostUrl.isEmpty)
                }
            }
        }
    }
}

// MARK: - Custom Title/Stats Header Bar View

struct HeaderBarView: View {
    let viewModel: SessionViewModel
    @Binding var showProgressDrawer: Bool
    @Binding var showSettings: Bool
    
    var body: some View {
        VStack(spacing: 0) {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    HStack(spacing: 8) {
                        HStack(spacing: 4) {
                            Circle()
                                .fill(viewModel.isConnected ? Color.green : (viewModel.isConnecting ? Color.orange : Color.red))
                                .frame(width: 6, height: 6)
                            Text(viewModel.statusText)
                                .font(.caption2)
                                .foregroundColor(.secondary)
                        }
                        
                        if !viewModel.currentAction.isEmpty {
                            Text("•")
                                .font(.caption2)
                                .foregroundColor(.secondary)
                            Text(viewModel.currentAction)
                                .font(.caption2)
                                .foregroundColor(.blue)
                                .lineLimit(1)
                        }
                    }
                }
                
                Spacer()

                // Session Performance Stats
                HStack(spacing: 12) {
                    VStack(alignment: .trailing, spacing: 2) {
                        if !viewModel.modelName.isEmpty {
                            Button { Task { await viewModel.runCommand("/models") } } label: {
                                Text(viewModel.modelName)
                                    .font(.caption2).fontWeight(.medium).lineLimit(1)
                                    .foregroundColor(.blue)
                            }
                            .disabled(viewModel.isBusy)
                        }
                        HStack(spacing: 6) {
                            if viewModel.contextPercent > 0 {
                                Text("ctx \(viewModel.contextPercent)%")
                                    .font(.caption2)
                                    .foregroundColor(viewModel.contextPercent >= 80 ? .orange : .secondary)
                            }
                            if viewModel.sessionCostUSD > 0 {
                                Text(String(format: "$%.3f", viewModel.sessionCostUSD))
                                    .font(.caption2).foregroundColor(.secondary)
                            } else {
                                Text("\(viewModel.tokensUsed) tok")
                                    .font(.caption2).foregroundColor(.secondary)
                            }
                        }
                        if !viewModel.gitBranch.isEmpty {
                            HStack(spacing: 3) {
                                Image(systemName: "arrow.triangle.branch").font(.system(size: 8))
                                Text(viewModel.gitBranch + (viewModel.gitDirty > 0 ? " •\(viewModel.gitDirty)" : ""))
                                    .font(.caption2).lineLimit(1)
                            }
                            .foregroundColor(.secondary)
                        }
                    }

                    // Sliding Drawer Toggle for Plan Tasks / Todos
                    Button {
                        withAnimation {
                            showProgressDrawer.toggle()
                        }
                    } label: {
                        Image(systemName: "checklist")
                            .font(.title3)
                            .foregroundColor(showProgressDrawer ? .blue : .primary)
                    }

                    // Configurations Marketplace Toggle (Workers, MCP, Skills)
                    Button {
                        showSettings = true
                    } label: {
                        Image(systemName: "slider.horizontal.3")
                            .font(.title3)
                    }
                }
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 10)
            .background(Color(uiColor: .systemBackground))
            
            Divider()
        }
    }
}

// MARK: - Sliding Progress Drawer

struct ProgressDrawer: View {
    let viewModel: SessionViewModel
    
    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Session Progress")
                .font(.headline)
                .padding(.bottom, 4)
            
            // Goals / Plan steps
            if !viewModel.activePlan.isEmpty {
                VStack(alignment: .leading, spacing: 8) {
                    Label("Current Plan", systemImage: "map.fill")
                        .font(.caption)
                        .fontWeight(.semibold)
                        .foregroundColor(.secondary)
                    
                    ForEach(viewModel.activePlan) { step in
                        HStack(alignment: .top, spacing: 8) {
                            Image(systemName: "circle")
                                .font(.caption2)
                                .foregroundColor(.blue)
                                .padding(.top, 2)
                            
                            VStack(alignment: .leading, spacing: 2) {
                                Text(step.title)
                                    .font(.subheadline)
                                    .fontWeight(.medium)
                                if let detail = step.detail {
                                    Text(detail)
                                        .font(.caption)
                                        .foregroundColor(.secondary)
                                }
                            }
                        }
                    }
                }
                Divider()
            }
            
            // Live Todos list
            VStack(alignment: .leading, spacing: 10) {
                Label("Action Checklist", systemImage: "checklist")
                    .font(.caption)
                    .fontWeight(.semibold)
                    .foregroundColor(.secondary)
                
                if viewModel.activeTodos.isEmpty {
                    Text("No task items currently active")
                        .font(.caption)
                        .foregroundColor(.secondary)
                        .padding(.vertical, 8)
                } else {
                    ScrollView {
                        VStack(alignment: .leading, spacing: 10) {
                            ForEach(viewModel.activeTodos) { todo in
                                HStack(alignment: .top, spacing: 8) {
                                    Image(systemName: todoIcon(todo.status))
                                        .foregroundColor(todoColor(todo.status))
                                        .font(.subheadline)
                                    
                                    Text(todo.content)
                                        .font(.subheadline)
                                        .strikethrough(todo.status == "completed")
                                        .foregroundColor(todo.status == "completed" ? .secondary : .primary)
                                }
                            }
                        }
                    }
                }
            }
            
            Spacer()
        }
        .padding()
        .background(Color(uiColor: .secondarySystemBackground))
        .border(Color(uiColor: .separator), width: 0.5)
    }
    
    private func todoIcon(_ status: String) -> String {
        switch status {
        case "completed": return "checkmark.circle.fill"
        case "in_progress": return "play.circle.fill"
        case "cancelled": return "xmark.circle.fill"
        default: return "circle"
        }
    }
    
    private func todoColor(_ status: String) -> Color {
        switch status {
        case "completed": return .green
        case "in_progress": return .orange
        case "cancelled": return .gray
        default: return .secondary
        }
    }
}

// MARK: - Menu Picker Sheet (model / reasoning / route selection)

struct MenuPickerSheet: View {
    let menu: MenuWire
    let viewModel: SessionViewModel
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            List(menu.items) { item in
                Button {
                    Task { await viewModel.selectMenuItem(item) }
                    dismiss()
                } label: {
                    HStack(alignment: .top, spacing: 10) {
                        Image(systemName: (item.current ?? false) ? "checkmark.circle.fill" : "circle")
                            .foregroundColor((item.current ?? false) ? .blue : .secondary)
                            .padding(.top, 2)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(item.label).font(.body).foregroundColor(.primary)
                            if let desc = item.desc, !desc.isEmpty {
                                Text(desc).font(.caption).foregroundColor(.secondary)
                            }
                        }
                        Spacer()
                    }
                }
            }
            .navigationTitle(menu.title ?? "Select")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { viewModel.dismissMenu(); dismiss() }
                }
            }
        }
    }
}
