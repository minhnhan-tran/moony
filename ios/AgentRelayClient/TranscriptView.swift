import SwiftUI

public struct TranscriptView: View {
    let viewModel: SessionViewModel
    
    public var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 16) {
                    ForEach(viewModel.messages) { message in
                        MessageRowView(message: message, viewModel: viewModel)
                            .id(message.id)
                    }
                    
                    if viewModel.isConnecting {
                        HStack {
                            Spacer()
                            ProgressView()
                            Text("Connecting stream...")
                                .font(.caption)
                                .foregroundColor(.secondary)
                            Spacer()
                        }
                        .padding()
                    }
                }
                .padding()
            }
            .onChange(of: viewModel.messages.count) { oldValue, newValue in
                if let last = viewModel.messages.last {
                    withAnimation {
                        proxy.scrollTo(last.id, anchor: .bottom)
                    }
                }
            }
        }
        .background(Color(uiColor: .systemGroupedBackground))
    }
}

// MARK: - Message Row View

struct MessageRowView: View {
    let message: ChatMessage
    let viewModel: SessionViewModel
    
    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            if message.sender == .user {
                Spacer()
            } else {
                AvatarView(sender: message.sender)
            }
            
            VStack(alignment: message.sender == .user ? .trailing : .leading, spacing: 6) {
                // Sender & Time Header
                HStack(spacing: 6) {
                    Text(senderName(message.sender))
                        .font(.caption)
                        .fontWeight(.semibold)
                    Text(message.timestamp, style: .time)
                        .font(.caption2)
                        .foregroundColor(.secondary)
                }
                
                // Content Body
                VStack(alignment: .leading, spacing: 10) {
                    if let approval = message.approval {
                        ApprovalCardView(approval: approval, viewModel: viewModel)
                    } else if let question = message.question {
                        QuestionCardView(question: question, viewModel: viewModel)
                    } else {
                        // Standard Rich Chat Bubble
                        Text(try! AttributedString(markdown: message.content, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace)))
                            .font(.body)
                            .padding(12)
                            .background(bubbleColor(message.sender))
                            .foregroundColor(foregroundColor(message.sender))
                            .cornerRadius(12)
                    }
                }
            }
            
            if message.sender == .user {
                AvatarView(sender: .user)
            } else {
                Spacer()
            }
        }
    }
    
    private func senderName(_ sender: ChatMessage.MessageSender) -> String {
        switch sender {
        case .agent: return "SQE Code Agent"
        case .user: return "You"
        case .system: return "System"
        }
    }
    
    private func bubbleColor(_ sender: ChatMessage.MessageSender) -> Color {
        switch sender {
        case .user: return .accentColor
        case .agent: return Color(uiColor: .secondarySystemGroupedBackground)
        case .system: return Color(uiColor: .tertiarySystemGroupedBackground)
        }
    }
    
    private func foregroundColor(_ sender: ChatMessage.MessageSender) -> Color {
        switch sender {
        case .user: return .white
        default: return .primary
        }
    }
}

// MARK: - Avatar View

struct AvatarView: View {
    let sender: ChatMessage.MessageSender
    
    var body: some View {
        ZStack {
            Circle()
                .fill(color)
                .frame(width: 32, height: 32)
            
            Image(systemName: iconName)
                .resizable()
                .scaledToFit()
                .frame(width: 16, height: 16)
                .foregroundColor(.white)
        }
    }
    
    private var color: Color {
        switch sender {
        case .agent: return .purple
        case .user: return .blue
        case .system: return .orange
        }
    }
    
    private var iconName: String {
        switch sender {
        case .agent: return "cpu.fill"
        case .user: return "person.fill"
        case .system: return "terminal.fill"
        }
    }
}

// MARK: - Interactive Cards (Approvals & Questions)

struct ApprovalCardView: View {
    let approval: ClientApproval
    let viewModel: SessionViewModel
    
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            // Amber Warning Banner
            HStack(spacing: 8) {
                Image(systemName: "exclamationmark.triangle.fill")
                    .foregroundColor(.amber)
                Text("Permission Request")
                    .fontWeight(.bold)
                    .foregroundColor(.amber)
            }
            
            Text("The agent wants to execute tool **\(approval.toolName)**.")
                .font(.subheadline)
            
            // Arguments Section
            DisclosureGroup("Show Arguments") {
                ScrollView {
                    Text(approval.arguments)
                        .font(.system(.caption, design: .monospaced))
                        .padding(8)
                        .background(Color.black.opacity(0.05))
                        .cornerRadius(6)
                }
                .frame(maxHeight: 120)
            }
            .font(.caption)
            .foregroundColor(.secondary)
            
            // Diffs Box (If file modifications)
            if let diff = approval.diff, !diff.isEmpty {
                VStack(alignment: .leading, spacing: 4) {
                    Text("File Modifications:")
                        .font(.caption)
                        .fontWeight(.semibold)
                    CodeDiffView(diffText: diff)
                }
            }
            
            // Interactive Buttons
            if approval.isAnswered {
                HStack {
                    Image(systemName: "checkmark.circle.fill")
                        .foregroundColor(.green)
                    Text("Action submitted: \(approval.selectedAnswer ?? "")")
                        .font(.caption)
                        .foregroundColor(.secondary)
                }
                .padding(.top, 4)
            } else {
                HStack(spacing: 12) {
                    Button {
                        Task { await viewModel.submitApproval(id: approval.id, approve: true) }
                    } label: {
                        Text("Approve")
                            .fontWeight(.medium)
                            .frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.borderedProminent)
                    .tint(.green)
                    
                    Button {
                        Task { await viewModel.submitApproval(id: approval.id, approve: true, always: true) }
                    } label: {
                        Text("Always Allow")
                            .fontWeight(.medium)
                            .frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.bordered)
                    .tint(.blue)
                    
                    Button {
                        Task { await viewModel.submitApproval(id: approval.id, approve: false) }
                    } label: {
                        Text("Deny")
                            .fontWeight(.medium)
                            .frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.bordered)
                    .tint(.red)
                }
                .padding(.top, 6)
            }
        }
        .padding(14)
        .background(Color(uiColor: .secondarySystemGroupedBackground))
        .cornerRadius(12)
        .overlay(
            RoundedRectangle(cornerRadius: 12)
                .stroke(Color.amber, lineWidth: 2)
        )
    }
}

struct QuestionCardView: View {
    let question: ClientQuestion
    let viewModel: SessionViewModel
    @State private var selections: Set<String> = []
    
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 8) {
                Image(systemName: "questionmark.bubble.fill")
                    .foregroundColor(.blue)
                Text("Agent Question")
                    .fontWeight(.bold)
                    .foregroundColor(.blue)
            }
            
            Text(question.text)
                .font(.subheadline)
                
            if question.isAnswered {
                VStack(alignment: .leading, spacing: 6) {
                    Text("Your answer:")
                        .font(.caption)
                        .foregroundColor(.secondary)
                    ForEach(Array(question.selectedOptions), id: \.self) { opt in
                        HStack {
                            Image(systemName: "checkmark.circle.fill")
                                .foregroundColor(.blue)
                            Text(opt)
                        }
                    }
                }
                .padding(.top, 4)
            } else {
                // Choice selection
                VStack(alignment: .leading, spacing: 8) {
                    ForEach(question.options, id: \.self) { option in
                        Button {
                            if question.multiple {
                                if selections.contains(option) {
                                    selections.remove(option)
                                } else {
                                    selections.insert(option)
                                }
                            } else {
                                selections = [option]
                            }
                        } label: {
                            HStack {
                                Image(systemName: selections.contains(option) ? "checkmark.square.fill" : "square")
                                    .foregroundColor(.blue)
                                Text(option)
                                    .foregroundColor(.primary)
                                Spacer()
                            }
                        }
                    }
                }
                
                Button {
                    Task { await viewModel.submitAnswer(id: question.id, selectedOptions: Array(selections)) }
                } label: {
                    Text("Submit Answer")
                        .fontWeight(.bold)
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .disabled(selections.isEmpty)
                .padding(.top, 6)
            }
        }
        .padding(14)
        .background(Color(uiColor: .secondarySystemGroupedBackground))
        .cornerRadius(12)
        .overlay(
            RoundedRectangle(cornerRadius: 12)
                .stroke(Color.blue.opacity(0.5), lineWidth: 1)
        )
    }
}

// MARK: - Code Diff Viewer

struct CodeDiffView: View {
    let diffText: String
    
    var body: some View {
        ScrollView(.horizontal, showsIndicators: true) {
            VStack(alignment: .leading, spacing: 0) {
                ForEach(diffText.components(separatedBy: .newlines), id: \.self) { line in
                    HStack(spacing: 8) {
                        Text(linePrefix(line))
                            .frame(width: 24, alignment: .center)
                            .font(.system(.caption2, design: .monospaced))
                            .foregroundColor(.secondary)
                        
                        Text(line)
                            .font(.system(.caption2, design: .monospaced))
                            .foregroundColor(lineColor(line))
                    }
                    .padding(.horizontal, 6)
                    .padding(.vertical, 2)
                    .background(lineBackground(line))
                }
            }
        }
        .background(Color.black.opacity(0.05))
        .cornerRadius(8)
    }
    
    private func linePrefix(_ line: String) -> String {
        if line.hasPrefix("+") { return "+" }
        if line.hasPrefix("-") { return "-" }
        return " "
    }
    
    private func lineColor(_ line: String) -> Color {
        if line.hasPrefix("+") { return .green }
        if line.hasPrefix("-") { return .red }
        return .primary
    }
    
    private func lineBackground(_ line: String) -> Color {
        if line.hasPrefix("+") { return Color.green.opacity(0.12) }
        if line.hasPrefix("-") { return Color.red.opacity(0.12) }
        return .clear
    }
}

// MARK: - Standard Amber Color Extension

extension Color {
    static let amber = Color(red: 0.96, green: 0.65, blue: 0.14)
}
