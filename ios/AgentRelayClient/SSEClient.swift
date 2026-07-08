import Foundation
import Observation
import SwiftUI
import AuthenticationServices

// MARK: - SSE Event Parser

public enum SSEError: Error {
    case invalidURL
    case badResponse(statusCode: Int)
    case connectionLost
}

// MARK: - Session View Model

@Observable
@MainActor
public final class SessionViewModel {
    public var messages: [ChatMessage] = []
    public var activeTodos: [TodoItem] = []
    public var activePlan: [PlanStep] = []
    public var mcpLogs: [MCPLog] = []
    
    public var isConnected = false
    public var isConnecting = false
    public var statusText = "Ready"
    public var currentAction = ""
    public var tokensUsed = 0
    public var elapsedSeconds: Double = 0.0

    // Rich status (mirrors the web header/status bar).
    public var modelName = ""
    public var reasoning = ""
    public var contextPercent = 0
    public var sessionCostUSD = 0.0
    public var gitBranch = ""
    public var gitDirty = 0
    public var agentName = ""
    public var approvalMode = ""

    // Authoritative session state from the relay ("idle" | "busy" |
    // "awaiting_approval"); drives the composer's send/interrupt toggle.
    public var sessionState = "idle"
    public var isBusy: Bool { sessionState != "idle" }

    // Pending messages queued while a turn is running (FIFO).
    public var queuedMessages: [String] = []

    // Composer affordances streamed by the relay (fall back to built-ins if empty).
    public var slashCommands: [CmdWire] = []
    public var mentions: [MentionWire] = []

    // A picker the agent requested (e.g. /model). Presented as a sheet; selecting
    // sends "{command} {value}" back as a message.
    public var pendingMenu: MenuWire?
    
    public var hostProfile: HostProfile?
    public var laptopBase: String = ""
    public var activeSessionId: String?
    
    private let eventStreamTaskHolder = TaskHolder()

    // Ids of the assistant text / thinking bubbles currently being streamed into,
    // so successive deltas (live) or replayed blocks (history) accumulate into one
    // message. Reset when a tool call, a new user turn, or a stream reset arrives —
    // mirroring the web UI's endAssistant() boundaries.
    private var currentAssistantId: String? = nil
    private var currentThinkingId: String? = nil
    
    public init(hostProfile: HostProfile? = nil, sessionId: String? = nil) {
        self.hostProfile = hostProfile
        self.activeSessionId = sessionId
    }
    
    deinit {
        eventStreamTaskHolder.cancel()
    }
    
    // MARK: - Connection & SSE Loop
    
    public func connect(host: HostProfile, laptopBase: String = "", sessionId: String) {
        disconnect()
        self.hostProfile = host
        self.laptopBase = laptopBase
        self.activeSessionId = sessionId
        self.isConnecting = true
        self.statusText = "Connecting..."
        
        // Clear previous session states
        self.messages = []
        self.activeTodos = []
        self.activePlan = []
        self.mcpLogs = []
        self.tokensUsed = 0
        self.elapsedSeconds = 0.0
        self.currentAction = ""
        self.currentAssistantId = nil
        self.currentThinkingId = nil
        self.queuedMessages = []
        self.pendingMenu = nil
        self.sessionState = "idle"
        self.contextPercent = 0
        self.sessionCostUSD = 0.0
        
        let urlString = "\(host.hostUrlString)\(laptopBase)/api/sessions/\(sessionId)/events"
        guard let url = URL(string: urlString) else {
            self.isConnecting = false
            self.statusText = "Error: Invalid Host URL"
            return
        }
        
        let task = Task { [weak self] in
            guard let self = self else { return }
            var request = URLRequest(url: url)
            request.timeoutInterval = 3600 // High timeout for stream
            request.setValue("text/event-stream", forHTTPHeaderField: "Accept")
            request.setValue("no-cache", forHTTPHeaderField: "Cache-Control")
            if !host.apiToken.isEmpty {
                request.setValue("Bearer \(host.apiToken)", forHTTPHeaderField: "Authorization")
            }
            
            do {
                let (bytes, response) = try await URLSession.trusted.bytes(for: request)
                guard let httpResponse = response as? HTTPURLResponse else {
                    throw SSEError.connectionLost
                }
                
                guard httpResponse.statusCode == 200 else {
                    throw SSEError.badResponse(statusCode: httpResponse.statusCode)
                }

                // If auth gates the relay and we have no valid session cookie/token,
                // the request gets 302'd to the HTML login page, which URLSession
                // silently follows to a 200 — so the "stream" is actually HTML and
                // no events ever arrive. Detect that by the content type and surface
                // it instead of showing an empty transcript forever.
                let contentType = (httpResponse.value(forHTTPHeaderField: "Content-Type") ?? "").lowercased()
                guard contentType.contains("text/event-stream") else {
                    self.isConnected = false
                    self.isConnecting = false
                    self.statusText = "Not authenticated — sign in to this host again"
                    return
                }

                self.isConnected = true
                self.isConnecting = false
                self.statusText = "Connected"

                // The relay frames every event as `data: <json>\n\n` with NO
                // `event:` line — the type is the JSON `type` field (see
                // internal/web/server.go writeSSE). We split the byte stream into
                // lines ourselves (on `\n`), because SSE dispatch hinges on the
                // BLANK line between events and AsyncBytes.lines does not reliably
                // surface empty lines — if it swallows them, events never dispatch.
                //
                // Each line's bytes are decoded as UTF-8 in one shot (not byte by
                // byte as UnicodeScalar, which corrupted multi-byte characters).
                var dataBuffer = ""
                var lineBytes = [UInt8]()
                for try await byte in bytes {
                    if byte == 0x0A { // "\n"
                        if lineBytes.last == 0x0D { lineBytes.removeLast() } // strip "\r"
                        self.handleLine(String(decoding: lineBytes, as: UTF8.self), dataBuffer: &dataBuffer)
                        lineBytes.removeAll(keepingCapacity: true)
                    } else {
                        lineBytes.append(byte)
                    }
                }
            } catch {
                self.isConnected = false
                self.isConnecting = false
                self.statusText = "Disconnected: \(error.localizedDescription)"
            }
        }
        eventStreamTaskHolder.set(task)
    }
    
    public func disconnect() {
        eventStreamTaskHolder.cancel()
        self.isConnected = false
        self.isConnecting = false
        self.statusText = "Disconnected"
    }
    
    private func handleLine(_ line: String, dataBuffer: inout String) {
        if line.isEmpty {
            // Blank line terminates an event: decode + dispatch the accumulated data.
            if !dataBuffer.isEmpty {
                dispatchSSEEvent(data: dataBuffer)
                dataBuffer = ""
            }
            return
        }
        if line.hasPrefix(":") {
            return // SSE comment / keepalive
        }
        guard let colon = line.firstIndex(of: ":") else { return }
        let field = line[..<colon]
        // We only care about `data:`. The relay never sends `event:` (type is in
        // the JSON), and `id:` / `retry:` are unused.
        guard field == "data" else { return }
        var value = line[line.index(after: colon)...]
        if value.first == " " { value = value.dropFirst() } // strip the single space after "data:"
        if !dataBuffer.isEmpty { dataBuffer.append("\n") }
        dataBuffer.append(contentsOf: value)
    }

    private func dispatchSSEEvent(data: String) {
        guard let jsonData = data.data(using: .utf8) else { return }
        do {
            let payload = try JSONDecoder().decode(SSEPayload.self, from: jsonData)
            handlePayload(payload)
        } catch {
            print("Failed to decode SSE data: \(error). Data was: \(data)")
        }
    }

    // handlePayload dispatches on the JSON `type` field (the relay's discriminator),
    // mirroring the web UI's `switch (ev.type)` in app.js. Each case maps a wireEvent
    // to the client's flat transcript / panel state.
    private func handlePayload(_ p: SSEPayload) {
        switch p.type ?? "" {
        case "user":
            // A new turn begins: close any streaming assistant/thinking bubble.
            currentAssistantId = nil
            currentThinkingId = nil
            var content = p.text ?? ""
            if let atts = p.attachments, !atts.isEmpty {
                let chips = atts.map { "📎 \($0.name)" }.joined(separator: "  ")
                content = content.isEmpty ? chips : content + "\n\n" + chips
            }
            if !content.isEmpty {
                let msg = ChatMessage(
                    id: "user_" + UUID().uuidString,
                    sender: .user,
                    content: content,
                    timestamp: parseTime(p.time),
                    isIncremental: false
                )
                messages.append(msg)
            }

        case "text":
            currentThinkingId = nil // a prose block closes any open thinking bubble
            appendStreaming(&currentAssistantId, sender: .agent, delta: p.text ?? "")

        case "thinking":
            currentAssistantId = nil
            let t = p.text ?? ""
            if t.isEmpty || t == "(reasoning hidden by the provider)" { break }
            // First chunk gets the "Thinking:" lead-in; later chunks append raw.
            if currentThinkingId == nil {
                appendStreaming(&currentThinkingId, sender: .agent, delta: "💭 Thinking: " + t)
            } else {
                appendStreaming(&currentThinkingId, sender: .agent, delta: t)
            }

        case "stream_reset":
            // A transient stream error is being retried: drop the partial bubbles so
            // the re-streamed answer replaces them cleanly.
            removeMessage(id: currentAssistantId)
            removeMessage(id: currentThinkingId)
            currentAssistantId = nil
            currentThinkingId = nil

        case "tool_start":
            currentAssistantId = nil
            currentThinkingId = nil
            guard let toolId = p.toolId else { break }
            let name = p.tool ?? "tool"
            let prefix = (p.parentId != nil) ? "↳ " : ""
            var body = "\(prefix)🔧 **\(name)**"
            if let arg = toolArgPreview(p) {
                body += "\n```\n\(arg)\n```"
            }
            let msg = ChatMessage(
                id: "tool_" + toolId,
                sender: .system,
                content: body,
                timestamp: Date(),
                isIncremental: false
            )
            appendOrReplace(msg)

        case "tool_end":
            guard let toolId = p.toolId else { break }
            let id = "tool_" + toolId
            if let idx = messages.firstIndex(where: { $0.id == id }) {
                var body = messages[idx].content
                let out = (p.output ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
                if !out.isEmpty {
                    let clipped = clip(out, 1200)
                    let label = (p.isError ?? false) ? "error" : "result"
                    body += "\n\n_\(label):_\n```\n\(clipped)\n```"
                }
                let prev = messages[idx]
                messages[idx] = ChatMessage(
                    id: prev.id, sender: prev.sender, content: body,
                    timestamp: prev.timestamp, isIncremental: false
                )
            }

        case "approval":
            currentAssistantId = nil
            currentThinkingId = nil
            let appId = "approve_" + UUID().uuidString
            let approval = ClientApproval(
                id: appId,
                toolName: p.approval?.tool ?? p.tool ?? "tool",
                arguments: p.approval?.input ?? p.input ?? "{}",
                diff: p.approval?.diff ?? p.diff
            )
            let msg = ChatMessage(
                id: appId,
                sender: .system,
                content: "Requested execution of \(approval.toolName)",
                timestamp: Date(),
                approval: approval,
                isIncremental: false
            )
            messages.append(msg)

        case "question":
            currentAssistantId = nil
            currentThinkingId = nil
            guard let q = p.question, let qText = q.question else { break }
            let qId = "ask_" + UUID().uuidString
            let question = ClientQuestion(
                id: qId,
                text: qText,
                options: (q.options ?? []).map { $0.label },
                multiple: q.multiple ?? false
            )
            let msg = ChatMessage(
                id: qId,
                sender: .system,
                content: qText,
                timestamp: Date(),
                question: question,
                isIncremental: false
            )
            messages.append(msg)

        case "plan":
            self.activePlan = p.plan?.steps ?? []

        case "todos":
            self.activeTodos = p.todos ?? []

        case "status":
            if let s = p.status {
                if let m = s.model { self.modelName = m }
                if let r = s.reasoning { self.reasoning = r }
                if let a = s.agent { self.agentName = a }
                if let ap = s.approval { self.approvalMode = ap }
                if let cp = s.contextPercent { self.contextPercent = cp }
                if let cost = s.costUSD { self.sessionCostUSD = cost }
                if let gb = s.gitBranch { self.gitBranch = gb }
                if let gd = s.gitDirty { self.gitDirty = gd }
                if let tin = s.tokensIn, let tout = s.tokensOut {
                    self.tokensUsed = tin + tout
                }
                self.currentAction = s.activeTool ?? ""
            }

        case "state":
            if let st = p.state {
                self.sessionState = st
                self.currentAction = (st == "idle") ? "" : self.currentAction
            }

        case "commands":
            if let cmds = p.commands { self.slashCommands = cmds }
            if let ms = p.mentions { self.mentions = ms }

        case "menu":
            if let menu = p.menu { self.pendingMenu = menu }

        case "queue":
            self.queuedMessages = p.queue ?? []

        case "info", "title":
            if let t = p.text, !t.isEmpty {
                appendSystem(id: nil, content: "ℹ️ " + t)
            }

        case "error":
            if let t = p.text, !t.isEmpty {
                appendSystem(id: nil, content: "⚠️ " + t)
            }

        case "board_message":
            // A multi-agent message-board post (agent → agent, or broadcast).
            currentAssistantId = nil
            currentThinkingId = nil
            if let b = p.board {
                let route = b.to.isEmpty ? b.from : "\(b.from) → \(b.to)"
                appendSystem(id: nil, content: "💬 **\(route)**\n\(b.text)")
            }

        case "worker":
            // Live worker progress, keyed by worker tool id so updates coalesce.
            guard let id = p.toolId else { break }
            let name = p.tool ?? "worker"
            let detail = [p.summary, p.text].compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: " — ")
            appendSystem(id: "worker_" + id, content: "⚙️ \(name)" + (detail.isEmpty ? "" : ": \(detail)"))

        case "turn_done", "step", "context", "reset", "sessions", "turn_meta", "task_meta":
            break

        default:
            // Unhandled event types are ignored. Decode failures are logged in
            // dispatchSSEEvent; add a case here to surface a new event.
            break
        }
    }

    // appendStreaming accumulates a text/thinking delta into the bubble identified
    // by `id`, creating it (with a fresh id) on the first chunk.
    private func appendStreaming(_ id: inout String?, sender: ChatMessage.MessageSender, delta: String) {
        if delta.isEmpty { return }
        if let existing = id, let idx = messages.firstIndex(where: { $0.id == existing }) {
            let prev = messages[idx]
            messages[idx] = ChatMessage(
                id: prev.id, sender: prev.sender, content: prev.content + delta,
                timestamp: prev.timestamp, approval: prev.approval, question: prev.question,
                isIncremental: true
            )
        } else {
            let newId = "stream_" + UUID().uuidString
            id = newId
            messages.append(ChatMessage(
                id: newId, sender: sender, content: delta,
                timestamp: Date(), isIncremental: true
            ))
        }
    }

    private func appendOrReplace(_ msg: ChatMessage) {
        if let idx = messages.firstIndex(where: { $0.id == msg.id }) {
            messages[idx] = msg
        } else {
            messages.append(msg)
        }
    }

    // appendSystem appends (or, with a stable id, updates) a system bubble — used
    // for info/error/board/worker lines. A nil id always appends a fresh entry.
    private func appendSystem(id: String?, content: String) {
        let msgId = id ?? ("sys_" + UUID().uuidString)
        appendOrReplace(ChatMessage(id: msgId, sender: .system, content: content, timestamp: Date(), isIncremental: false))
    }

    private func removeMessage(id: String?) {
        guard let id else { return }
        messages.removeAll { $0.id == id }
    }

    // toolArgPreview renders a short, readable preview of a tool call's input —
    // the diff if present, else the (clipped) raw JSON args.
    private func toolArgPreview(_ p: SSEPayload) -> String? {
        if let d = p.diff, !d.isEmpty { return clip(d, 1200) }
        if let summary = p.summary, !summary.isEmpty { return summary }
        if let input = p.input, !input.isEmpty, input != "{}" { return clip(input, 600) }
        return nil
    }

    private func clip(_ s: String, _ max: Int) -> String {
        s.count <= max ? s : String(s.prefix(max)) + "…"
    }

    // parseTime decodes an RFC3339 timestamp from a "user" event, falling back to now.
    private func parseTime(_ s: String?) -> Date {
        guard let s, !s.isEmpty else { return Date() }
        return ISO8601DateFormatter().date(from: s) ?? Date()
    }
    
    // MARK: - Outbound Actions (REST POST)

    public func sendMessage(_ text: String, attachments: [OutboundAttachment] = []) async -> Bool {
        guard let host = hostProfile, let sessionId = activeSessionId else { return false }

        let urlString = "\(host.hostUrlString)\(laptopBase)/api/sessions/\(sessionId)/message"
        guard let url = URL(string: urlString) else { return false }

        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        if !host.apiToken.isEmpty {
            request.setValue("Bearer \(host.apiToken)", forHTTPHeaderField: "Authorization")
        }

        // The relay's /message accepts text plus base64 attachments (images/docs/
        // files) — see handleMessage in internal/web/server.go.
        let body = MessageBody(
            text: text,
            attachments: attachments.map {
                MessageBody.Attachment(name: $0.name, mediaType: $0.mediaType, data: $0.dataBase64)
            }
        )
        request.httpBody = try? JSONEncoder().encode(body)

        // Don't echo the message locally: the relay publishes a "user" event for the
        // turn (session.go / mirror.go), which the SSE stream renders. A local append
        // would duplicate it.

        do {
            let (_, response) = try await URLSession.trusted.data(for: request)
            guard let httpResponse = response as? HTTPURLResponse, (200...299).contains(httpResponse.statusCode) else {
                return false
            }
            return true
        } catch {
            print("Failed to send message: \(error)")
            return false
        }
    }

    public func submitApproval(id: String, approve: Bool, always: Bool = false) async -> Bool {
        guard let host = hostProfile, let sessionId = activeSessionId else { return false }

        let urlString = "\(host.hostUrlString)\(laptopBase)/api/sessions/\(sessionId)/approve"
        guard let url = URL(string: urlString) else { return false }

        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        if !host.apiToken.isEmpty {
            request.setValue("Bearer \(host.apiToken)", forHTTPHeaderField: "Authorization")
        }

        // The relay holds a single pending approval per session and expects
        // {"decision": "approve"|"always"|"deny"} (see handleApprove); `id` is only
        // used locally to mark the right card answered.
        let decision = approve ? (always ? "always" : "approve") : "deny"
        let body: [String: String] = ["decision": decision]
        request.httpBody = try? JSONEncoder().encode(body)

        do {
            let (_, response) = try await URLSession.trusted.data(for: request)
            guard let httpResponse = response as? HTTPURLResponse, (200...299).contains(httpResponse.statusCode) else {
                return false
            }

            // Update local message state
            if let index = messages.firstIndex(where: { $0.approval?.id == id }) {
                var updatedMsg = messages[index]
                updatedMsg.approval?.isAnswered = true
                updatedMsg.approval?.selectedAnswer = decision
                messages[index] = updatedMsg
            }
            return true
        } catch {
            print("Failed to submit approval: \(error)")
            return false
        }
    }

    public func submitAnswer(id: String, selectedOptions: [String]) async -> Bool {
        guard let host = hostProfile, let sessionId = activeSessionId else { return false }

        let urlString = "\(host.hostUrlString)\(laptopBase)/api/sessions/\(sessionId)/answer"
        guard let url = URL(string: urlString) else { return false }

        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        if !host.apiToken.isEmpty {
            request.setValue("Bearer \(host.apiToken)", forHTTPHeaderField: "Authorization")
        }

        // The relay expects {"labels": [...]} (the selected option labels) — see
        // handleAnswer.
        let body: [String: [String]] = ["labels": selectedOptions]
        request.httpBody = try? JSONEncoder().encode(body)

        do {
            let (_, response) = try await URLSession.trusted.data(for: request)
            guard let httpResponse = response as? HTTPURLResponse, (200...299).contains(httpResponse.statusCode) else {
                return false
            }

            // Update local message state
            if let index = messages.firstIndex(where: { $0.question?.id == id }) {
                var updatedMsg = messages[index]
                updatedMsg.question?.isAnswered = true
                updatedMsg.question?.selectedOptions = Set(selectedOptions)
                messages[index] = updatedMsg
            }
            return true
        } catch {
            print("Failed to submit answer: \(error)")
            return false
        }
    }

    public func interrupt() async -> Bool {
        guard let host = hostProfile, let sessionId = activeSessionId else { return false }

        let urlString = "\(host.hostUrlString)\(laptopBase)/api/sessions/\(sessionId)/interrupt"
        guard let url = URL(string: urlString) else { return false }

        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        if !host.apiToken.isEmpty {
            request.setValue("Bearer \(host.apiToken)", forHTTPHeaderField: "Authorization")
        }

        do {
            let (_, response) = try await URLSession.trusted.data(for: request)
            guard let httpResponse = response as? HTTPURLResponse, (200...299).contains(httpResponse.statusCode) else {
                return false
            }
            return true
        } catch {
            print("Failed to interrupt: \(error)")
            return false
        }
    }

    // selectMenuItem answers a picker the agent requested: the relay expects the
    // choice as a plain message "{command} {value}" (mirrors the web UI).
    public func selectMenuItem(_ item: MenuItemWire) async {
        guard let menu = pendingMenu else { return }
        pendingMenu = nil
        _ = await sendMessage("\(menu.command) \(item.value)")
    }

    public func dismissMenu() { pendingMenu = nil }

    // runCommand sends a slash command (or any text) as a message.
    public func runCommand(_ command: String) async {
        _ = await sendMessage(command)
    }
}

// MARK: - SSL Trust Custom Session Extension

public extension URLSession {
    static var trusted: URLSession {
        struct Static {
            static let session: URLSession = {
                let configuration = URLSessionConfiguration.default
                configuration.timeoutIntervalForRequest = 3600
                configuration.timeoutIntervalForResource = 3600
                return URLSession(configuration: configuration, delegate: SSLTrustDelegate(), delegateQueue: nil)
            }()
        }
        return Static.session
    }
}

class SSLTrustDelegate: NSObject, URLSessionDelegate {
    func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge, completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        if challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust {
            if let serverTrust = challenge.protectionSpace.serverTrust {
                let host = challenge.protectionSpace.host
                if host.contains("localhost") || host.contains("127.0.0.1") || host.contains("apple.com") {
                    completionHandler(.useCredential, URLCredential(trust: serverTrust))
                    return
                }
            }
        }
        completionHandler(.performDefaultHandling, nil)
    }
}

// MARK: - Native Web View for Sign-In

import WebKit

// AuthWebView drives the relay's OIDC sign-in inside a WKWebView:
//   load /auth/login → 302 to the IdMS authorize page → user authenticates →
//   IdMS redirects to /auth/callback?code=… → relay sets the session cookie and
//   302s back to "/". We detect completion when navigation lands back on the
//   relay's own host on a non-/auth path, copy the WKWebView cookies into the
//   shared store (so URLSession requests are authenticated), and call back.
//
// It surfaces load failures and a spinner instead of a blank white screen, which
// is what you get if (e.g.) the host URL is wrong, the TLS handshake is rejected,
// or the network is unreachable and there's no error handling.
public struct AuthWebView: UIViewControllerRepresentable {
    public let url: URL
    public let onLoginSuccess: () -> Void

    public init(url: URL, onLoginSuccess: @escaping () -> Void) {
        self.url = url
        self.onLoginSuccess = onLoginSuccess
    }

    public func makeCoordinator() -> Coordinator { Coordinator(self) }

    public func makeUIViewController(context: Context) -> UIViewController {
        let vc = UIViewController()
        vc.view.backgroundColor = .systemBackground

        let config = WKWebViewConfiguration()
        config.websiteDataStore = .default() // persistent, shared cookie store
        let webView = WKWebView(frame: .zero, configuration: config)
        webView.navigationDelegate = context.coordinator
        webView.translatesAutoresizingMaskIntoConstraints = false
        vc.view.addSubview(webView)

        let spinner = UIActivityIndicatorView(style: .large)
        spinner.translatesAutoresizingMaskIntoConstraints = false
        spinner.hidesWhenStopped = true
        vc.view.addSubview(spinner)

        let errorLabel = UILabel()
        errorLabel.translatesAutoresizingMaskIntoConstraints = false
        errorLabel.numberOfLines = 0
        errorLabel.textAlignment = .center
        errorLabel.textColor = .secondaryLabel
        errorLabel.font = .preferredFont(forTextStyle: .callout)
        errorLabel.isHidden = true
        vc.view.addSubview(errorLabel)

        NSLayoutConstraint.activate([
            webView.topAnchor.constraint(equalTo: vc.view.topAnchor),
            webView.bottomAnchor.constraint(equalTo: vc.view.bottomAnchor),
            webView.leadingAnchor.constraint(equalTo: vc.view.leadingAnchor),
            webView.trailingAnchor.constraint(equalTo: vc.view.trailingAnchor),
            spinner.centerXAnchor.constraint(equalTo: vc.view.centerXAnchor),
            spinner.centerYAnchor.constraint(equalTo: vc.view.centerYAnchor),
            errorLabel.centerXAnchor.constraint(equalTo: vc.view.centerXAnchor),
            errorLabel.centerYAnchor.constraint(equalTo: vc.view.centerYAnchor),
            errorLabel.leadingAnchor.constraint(equalTo: vc.view.leadingAnchor, constant: 32),
            errorLabel.trailingAnchor.constraint(equalTo: vc.view.trailingAnchor, constant: -32),
        ])

        context.coordinator.webView = webView
        context.coordinator.spinner = spinner
        context.coordinator.errorLabel = errorLabel
        context.coordinator.relayHost = url.host

        webView.load(URLRequest(url: url))
        return vc
    }

    public func updateUIViewController(_ uiViewController: UIViewController, context: Context) {}

    public class Coordinator: NSObject, WKNavigationDelegate {
        let parent: AuthWebView
        weak var webView: WKWebView?
        weak var spinner: UIActivityIndicatorView?
        weak var errorLabel: UILabel?
        var relayHost: String?
        private var succeeded = false

        init(_ parent: AuthWebView) { self.parent = parent }

        // Accept the relay's and IdMS's server trust (mirrors SSLTrustDelegate);
        // otherwise a self-signed or internal cert silently fails the load.
        public func webView(_ webView: WKWebView, didReceive challenge: URLAuthenticationChallenge, completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
            if challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
               let trust = challenge.protectionSpace.serverTrust {
                let host = challenge.protectionSpace.host
                if host.contains("apple.com") || host.contains("localhost") || host.contains("127.0.0.1") || host == relayHost {
                    completionHandler(.useCredential, URLCredential(trust: trust))
                    return
                }
            }
            completionHandler(.performDefaultHandling, nil)
        }

        public func webView(_ webView: WKWebView, didStartProvisionalNavigation navigation: WKNavigation!) {
            errorLabel?.isHidden = true
            spinner?.startAnimating()
        }

        public func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
            spinner?.stopAnimating()
            guard let current = webView.url, let relayHost else { return }
            // Sign-in is complete once we're back on the relay's own host (not the
            // IdMS host) on a non-/auth path — the callback has set the session
            // cookie and redirected home. The whole IdMS flow happens on a
            // different host, so this can't fire mid-login.
            guard current.host == relayHost, !current.path.hasPrefix("/auth/login"),
                  !current.path.hasPrefix("/auth/callback"), !succeeded else { return }
            succeeded = true
            webView.configuration.websiteDataStore.httpCookieStore.getAllCookies { cookies in
                for cookie in cookies { HTTPCookieStorage.shared.setCookie(cookie) }
                DispatchQueue.main.async { self.parent.onLoginSuccess() }
            }
        }

        public func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
            showError(error)
        }

        public func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
            showError(error)
        }

        private func showError(_ error: Error) {
            let ns = error as NSError
            // A cancelled load is benign — it's what a server-side redirect looks like.
            if ns.domain == NSURLErrorDomain && ns.code == NSURLErrorCancelled { return }
            spinner?.stopAnimating()
            errorLabel?.text = "Couldn't load the sign-in page.\n\n\(error.localizedDescription)\n\nCheck the host URL and your network connection."
            errorLabel?.isHidden = false
            print("sign-in load failed: \(error)")
        }
    }
}

private final class TaskHolder: @unchecked Sendable {
    private let lock = NSLock()
    private var task: Task<Void, Never>?

    func set(_ newTask: Task<Void, Never>?) {
        lock.lock()
        defer { lock.unlock() }
        task = newTask
    }

    func cancel() {
        lock.lock()
        defer { lock.unlock() }
        task?.cancel()
        task = nil
    }
}

