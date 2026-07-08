import Foundation

// MARK: - API Response Types

public struct RelayInfo: Codable, Sendable {
    public let version: String
    public let commit: String?
}

public struct Session: Codable, Identifiable, Hashable, Sendable {
    public let id: String
    public let title: String?
    public let createdAt: Date?
    public let status: String?
    
    public init(id: String, title: String? = nil, createdAt: Date? = nil, status: String? = nil) {
        self.id = id
        self.title = title
        self.createdAt = createdAt
        self.status = status
    }
}

// MARK: - Server-Sent Event (SSE) Payloads
//
// The relay sends every event as a single SSE `data:` frame (no `event:` line);
// the event type lives in the JSON `type` field. These structs mirror the
// server's `wireEvent` and its nested types in `internal/web/wire.go` — keep the
// field names (and their JSON tags) in sync with that file. Every field is
// optional because a given event only populates the subset relevant to its type.

public struct SSEPayload: Codable, Sendable {
    // Discriminator — selects the handler. Matches wireEvent.Type ("user", "text",
    // "thinking", "tool_start", "tool_end", "plan", "todos", "status", "state",
    // "approval", "question", "info", "turn_meta", …).
    public let type: String?
    public let seq: Int?

    // Text-bearing events (user / text / thinking / info / title).
    public let text: String?
    public let time: String? // RFC3339 timestamp stamped on a "user" event

    // Tool events (tool_start / tool_end).
    public let tool: String?
    public let toolId: String?
    public let input: String?       // raw JSON args (tool_start)
    public let output: String?      // tool result (tool_end)
    public let diff: String?        // unified diff for file edits
    public let summary: String?
    public let isError: Bool?
    public let parentId: String?    // set for a delegated sub-agent's tool call
    public let agent: String?
    public let model: String?

    // Session state ("state" event): idle / busy / awaiting_approval.
    public let state: String?

    // Plan / todo panels.
    public let plan: PlanWire?
    public let todos: [TodoItem]?

    // Pending interactions.
    public let approval: ApprovalWire?   // "approval" event has no separate id
    public let question: QuestionWire?

    // Header / status bar snapshot.
    public let status: StatusWire?

    // User-turn attachments (images / docs / files).
    public let attachments: [AttachWire]?

    // Composer affordances streamed by the relay.
    public let commands: [CmdWire]?    // slash-command palette
    public let mentions: [MentionWire]? // @-mention candidates
    public let menu: MenuWire?          // model/reasoning picker etc.
    public let queue: [String]?         // pending queued messages (FIFO)

    // Step progress (loop step N of max).
    public let step: Int?
    public let maxSteps: Int?

    // Multi-agent message-board post.
    public let board: BoardWire?
}

// BoardWire is one shared-message-board post (sender, addressee, text).
public struct BoardWire: Codable, Sendable {
    public let from: String
    public let to: String
    public let text: String

    enum CodingKeys: String, CodingKey { case from, to, text }
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        from = (try? c.decode(String.self, forKey: .from)) ?? ""
        to = (try? c.decode(String.self, forKey: .to)) ?? ""
        text = (try? c.decode(String.self, forKey: .text)) ?? ""
    }
}

// MARK: - Nested wire types (mirror internal/web/wire.go)

public struct PlanWire: Codable, Sendable {
    public let title: String?
    public let status: String?
    public let steps: [PlanStep]?
}

public struct ApprovalWire: Codable, Sendable {
    public let tool: String?
    public let input: String?
    public let summary: String?
    public let diff: String?
}

public struct QuestionWire: Codable, Sendable {
    public let question: String?
    public let multiple: Bool?
    public let options: [QuestionOptionWire]?
}

public struct QuestionOptionWire: Codable, Sendable {
    public let label: String
    public let description: String?
}

public struct AttachWire: Codable, Sendable {
    public let name: String
    public let kind: String      // image | document | file
    public let dataUri: String?
}

public struct StatusWire: Codable, Sendable {
    public let model: String?
    public let provider: String?
    public let reasoning: String?
    public let agent: String?
    public let approval: String?
    public let workingDir: String?
    public let tokensIn: Int?
    public let tokensOut: Int?
    public let costUSD: Double?
    public let contextPercent: Int?
    public let activeTool: String?
    public let gitBranch: String?
    public let gitDirty: Int?
    public let gitAhead: Int?
    public let gitBehind: Int?
}

// CmdWire is one slash command for the composer palette (Name includes the "/").
public struct CmdWire: Codable, Sendable {
    public let name: String
    public let args: String?
    public let desc: String?
}

// MentionWire is one @-mention candidate (persona / worker / skill).
public struct MentionWire: Codable, Sendable {
    public let name: String
    public let kind: String?
    public let desc: String?
}

// MenuWire is a clickable picker (e.g. model/route). Selecting an item sends
// "{command} {value}" back as a message (mirrors the web UI).
public struct MenuWire: Codable, Sendable {
    public let title: String?
    public let command: String
    public let items: [MenuItemWire]
}

public struct MenuItemWire: Codable, Sendable, Identifiable {
    public var id: String { value }
    public let label: String
    public let desc: String?
    public let value: String
    public let current: Bool?
}

// MARK: - Outbound (request) types

// OutboundAttachment is one attachment to send with a message: base64-encoded
// bytes plus a name and media type. Mirrors the relay's /message attachment shape.
public struct OutboundAttachment: Sendable {
    public let name: String
    public let mediaType: String
    public let dataBase64: String
    public init(name: String, mediaType: String, dataBase64: String) {
        self.name = name
        self.mediaType = mediaType
        self.dataBase64 = dataBase64
    }
}

// MessageBody is the JSON body POSTed to /api/sessions/{id}/message.
struct MessageBody: Encodable {
    let text: String
    let attachments: [Attachment]
    struct Attachment: Encodable {
        let name: String
        let mediaType: String
        let data: String
    }
}

// MARK: - Submodels

public struct PlanStep: Codable, Identifiable, Hashable, Sendable {
    public var id: String { title }
    public let title: String
    public let detail: String?
}

public struct TodoItem: Codable, Identifiable, Hashable, Sendable {
    public var id: String { content }
    public let content: String
    public let status: String // "pending", "in_progress", "completed", "cancelled"
    public let priority: String? // "low", "medium", "high"
}

public struct MCPLog: Codable, Identifiable, Hashable, Sendable {
    public var id: String { timestamp.description + tool }
    public let tool: String
    public let durationMs: Int
    public let status: String // "success", "failed"
    public let timestamp: Date
}

// MARK: - Client Local Representations

public struct ChatMessage: Identifiable, Hashable, Sendable {
    public let id: String
    public let sender: MessageSender
    public let content: String
    public let timestamp: Date
    public var approval: ClientApproval?
    public var question: ClientQuestion?
    public var isIncremental: Bool
    
    public enum MessageSender: String, Codable, Sendable {
        case agent = "agent"
        case user = "user"
        case system = "system"
    }
}

public struct ClientApproval: Hashable, Sendable {
    public let id: String
    public let toolName: String
    public let arguments: String
    public let diff: String?
    public var isAnswered: Bool = false
    public var selectedAnswer: String? = nil // "yes", "no", "always"
}

public struct ClientQuestion: Hashable, Sendable {
    public let id: String
    public let text: String
    public let options: [String]
    public let multiple: Bool
    public var isAnswered: Bool = false
    public var selectedOptions: Set<String> = []
}

// MARK: - User Profile

public struct UserProfile: Codable, Hashable, Sendable {
    public let accountname: String
    public let displayName: String
    
    enum CodingKeys: String, CodingKey {
        case accountname
        case displayName = "display_name"
    }
}

// MARK: - Multi-Project Tree Models

public struct Laptop: Codable, Identifiable, Hashable, Sendable {
    public var id: String { host }
    public let host: String
    public let name: String
    public let base: String
    public let online: Bool?
}

public struct Project: Codable, Identifiable, Hashable, Sendable {
    public var id: String { key }
    public let key: String
    public let name: String
    public let dir: String
}


