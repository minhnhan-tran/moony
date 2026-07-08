import SwiftUI
import PhotosUI
import UniformTypeIdentifiers

public struct ComposerView: View {
    let viewModel: SessionViewModel

    @State private var text = ""
    @State private var selectedPhotoItem: PhotosPickerItem? = nil
    @State private var attachedFiles: [URL] = []
    @State private var attachedImages: [UIImage] = []
    @State private var showFileImporter = false
    @FocusState private var inputFocused: Bool

    // Built-in fallbacks used only until the relay streams its real command /
    // mention catalogs (the "commands" event).
    private let fallbackCommands = ["/help", "/reset", "/clear", "/model", "/agent", "/skills", "/mcps"]
    private let fallbackMentions = ["@build", "@plan", "@explore"]

    public init(viewModel: SessionViewModel) { self.viewModel = viewModel }

    public var body: some View {
        VStack(spacing: 8) {
            // Live autocomplete driven by the relay's command/mention catalogs.
            if let token = currentToken {
                if token.hasPrefix("/"), !commandSuggestions(token).isEmpty {
                    AutocompletePopover(items: commandSuggestions(token), prefix: "/") { insertToken($0 + " ") }
                } else if token.hasPrefix("@"), !mentionSuggestions(token).isEmpty {
                    AutocompletePopover(items: mentionSuggestions(token), prefix: "@") { insertToken($0 + " ") }
                }
            }

            // Pending queued messages (sent while a turn was running).
            if !viewModel.queuedMessages.isEmpty {
                HStack(spacing: 6) {
                    Image(systemName: "clock.badge.checkmark")
                        .font(.caption2).foregroundColor(.orange)
                    Text("\(viewModel.queuedMessages.count) queued")
                        .font(.caption2).foregroundColor(.secondary)
                    Spacer()
                }
                .padding(.horizontal, 16)
            }

            // Attachment preview strip
            if !attachedFiles.isEmpty || !attachedImages.isEmpty {
                AttachmentStripView(files: $attachedFiles, images: $attachedImages)
            }

            // Active primary-agent switcher. Mirrors the web composer's agent
            // legend: shows the selected agent in its accent color and, when
            // tapped, sends /agent to open the picker (same flow as the web UI).
            // Hidden when no agent is set, matching `.composer-agent:empty`.
            if !viewModel.agentName.isEmpty {
                HStack {
                    Button { Task { await viewModel.runCommand("/agent") } } label: {
                        Text(viewModel.agentName)
                            .font(.system(size: 13, weight: .semibold))
                            .foregroundColor(agentColor(viewModel.agentName))
                            .lineLimit(1).truncationMode(.tail)
                            .padding(.horizontal, 9).padding(.vertical, 3)
                            .background(Color(uiColor: .secondarySystemBackground))
                            .clipShape(RoundedRectangle(cornerRadius: 5))
                    }
                    .buttonStyle(.plain)
                    .disabled(viewModel.isBusy)
                    Spacer()
                }
                .padding(.horizontal, 16)
            }

            // Main input bar
            HStack(alignment: .center, spacing: 10) {
                Button { showFileImporter = true } label: {
                    Image(systemName: "paperclip").font(.title3).foregroundColor(.secondary)
                        .frame(width: 32, height: 32)
                        .contentShape(Rectangle())
                }

                PhotosPicker(selection: $selectedPhotoItem, matching: .images) {
                    Image(systemName: "photo").font(.title3).foregroundColor(.secondary)
                        .frame(width: 32, height: 32)
                        .contentShape(Rectangle())
                }
                .onChange(of: selectedPhotoItem) { oldValue, newValue in
                    guard let item = newValue else { return }
                    Task {
                        if let data = try? await item.loadTransferable(type: Data.self),
                           let image = UIImage(data: data) {
                            attachedImages.append(image)
                        }
                        selectedPhotoItem = nil
                    }
                }

                ZStack(alignment: .leading) {
                    if text.isEmpty {
                        Text("Type a message, /command or @mention…")
                            .foregroundColor(.secondary).font(.body)
                            .padding(.horizontal, 10).padding(.vertical, 8)
                    }
                    TextField("", text: $text, axis: .vertical)
                        .font(.body)
                        .focused($inputFocused)
                        .tint(inputAccent)
                        .lineLimit(1...5)
                        .padding(.horizontal, 10).padding(.vertical, 8)
                        .background(
                            RoundedRectangle(cornerRadius: 8)
                                .fill(Color(uiColor: .secondarySystemBackground))
                        )
                        .overlay(
                            RoundedRectangle(cornerRadius: 8)
                                .stroke(inputBorderColor, lineWidth: 1)
                        )
                        .animation(.easeInOut(duration: 0.15), value: borderAnimationKey)
                }

                // Send/interrupt driven by the authoritative session state.
                if viewModel.isBusy {
                    Button { Task { await viewModel.interrupt() } } label: {
                        ZStack {
                            Circle().fill(Color.red).frame(width: 32, height: 32)
                            Image(systemName: "stop.fill").foregroundColor(.white).font(.caption)
                        }
                    }
                } else {
                    Button { send() } label: {
                        ZStack {
                            Circle().fill(canSend ? Color.blue : Color.gray.opacity(0.3)).frame(width: 32, height: 32)
                            Image(systemName: "arrow.up")
                                .foregroundColor(canSend ? .white : .secondary)
                                .font(.subheadline).fontWeight(.bold)
                        }
                    }
                    .disabled(!canSend)
                }
            }
            .padding(.horizontal, 12).padding(.vertical, 8)
            .background(Color(uiColor: .systemBackground))
        }
        .fileImporter(isPresented: $showFileImporter, allowedContentTypes: [.item], allowsMultipleSelection: true) { result in
            if case .success(let urls) = result { attachedFiles.append(contentsOf: urls) }
        }
    }

    private var canSend: Bool {
        !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !attachedImages.isEmpty || !attachedFiles.isEmpty
    }

    // Active agent's accent color, or nil when no primary agent is set.
    private var agentTint: Color? {
        viewModel.agentName.isEmpty ? nil : agentColor(viewModel.agentName)
    }
    // Caret color: the agent color when one is active, else the brand accent —
    // mirrors `caret-color: var(--agent-color, var(--accent))` in app.css.
    private var inputAccent: Color { agentTint ?? .accentColor }
    // Input border: tinted to the agent color whenever a primary agent is
    // active (focused or not); otherwise a neutral edge that lifts to the
    // accent on focus. Mirrors `#input` / `#input:focus` border-color in app.css.
    private var inputBorderColor: Color {
        if let tint = agentTint { return tint }
        return inputFocused ? .accentColor : Color(uiColor: .separator)
    }
    // Stable key so the border tint animates only when the agent or focus
    // actually changes (Color isn't reliably Equatable for dynamic providers).
    private var borderAnimationKey: String { "\(viewModel.agentName)#\(inputFocused)" }

    private func send() {
        guard canSend else { return }
        let outText = text
        let images = attachedImages
        let files = attachedFiles
        text = ""; attachedImages = []; attachedFiles = []
        Task { _ = await viewModel.sendMessage(outText, attachments: buildAttachments(images: images, files: files)) }
    }

    private func buildAttachments(images: [UIImage], files: [URL]) -> [OutboundAttachment] {
        var out: [OutboundAttachment] = []
        for (i, img) in images.enumerated() {
            if let data = img.jpegData(compressionQuality: 0.8) {
                out.append(OutboundAttachment(name: "image\(i + 1).jpg", mediaType: "image/jpeg",
                                              dataBase64: data.base64EncodedString()))
            }
        }
        for url in files {
            let scoped = url.startAccessingSecurityScopedResource()
            if let data = try? Data(contentsOf: url) {
                out.append(OutboundAttachment(name: url.lastPathComponent, mediaType: mimeType(for: url),
                                              dataBase64: data.base64EncodedString()))
            }
            if scoped { url.stopAccessingSecurityScopedResource() }
        }
        return out
    }

    private func mimeType(for url: URL) -> String {
        if let t = UTType(filenameExtension: url.pathExtension), let mime = t.preferredMIMEType {
            return mime
        }
        return "application/octet-stream"
    }

    // currentToken is the word being typed (for autocomplete), or nil.
    private var currentToken: String? {
        guard let last = text.components(separatedBy: .whitespacesAndNewlines).last, !last.isEmpty else { return nil }
        return (last.hasPrefix("/") || last.hasPrefix("@")) ? last : nil
    }

    private func commandSuggestions(_ token: String) -> [String] {
        let all = viewModel.slashCommands.isEmpty ? fallbackCommands : viewModel.slashCommands.map { $0.name }
        let q = token.lowercased()
        return all.filter { $0.lowercased().hasPrefix(q) }
    }

    private func mentionSuggestions(_ token: String) -> [String] {
        let all = viewModel.mentions.isEmpty ? fallbackMentions : viewModel.mentions.map { "@" + $0.name }
        let q = token.lowercased()
        return all.filter { $0.lowercased().hasPrefix(q) }
    }

    private func insertToken(_ replacement: String) {
        var words = text.components(separatedBy: " ")
        if !words.isEmpty {
            words.removeLast()
            words.append(replacement)
            text = words.joined(separator: " ")
        } else {
            text = replacement
        }
    }

    // MARK: - Agent coloring (mirrors internal/web/static)

    // agentColor maps an agent name to its accent color exactly as the web UI
    // does: built-ins use semantic tokens (build = brand blue, plan = warn
    // amber); any other (custom) agent gets a stable hue derived from its name.
    // Values match the --c-brand / --c-warn tokens and the hsl() rule in app.css.
    private func agentColor(_ name: String) -> Color {
        switch name.lowercased() {
        case "build":
            return Color(uiColor: UIColor { $0.userInterfaceStyle == .dark
                ? UIColor(red: 88/255, green: 166/255, blue: 255/255, alpha: 1)
                : UIColor(red: 9/255,  green: 105/255, blue: 218/255, alpha: 1) })
        case "plan":
            return Color(uiColor: UIColor { $0.userInterfaceStyle == .dark
                ? UIColor(red: 210/255, green: 153/255, blue: 34/255, alpha: 1)
                : UIColor(red: 154/255, green: 103/255, blue: 0,      alpha: 1) })
        default:
            let h = agentHue(name)
            return Color(uiColor: UIColor { $0.userInterfaceStyle == .dark
                ? Self.hsl(h, 0.70, 0.60) : Self.hsl(h, 0.62, 0.42) })
        }
    }

    // agentHue reproduces app.js agentHue(): a deterministic 0–359 hue so the
    // same agent name always maps to the same color across launches.
    private func agentHue(_ name: String) -> Int {
        var h = 0
        for scalar in name.lowercased().unicodeScalars { h = (h * 31 + Int(scalar.value)) % 360 }
        return h
    }

    // hsl builds a UIColor from CSS HSL by converting to UIKit's HSB model.
    private static func hsl(_ hueDeg: Int, _ s: CGFloat, _ l: CGFloat) -> UIColor {
        let v = l + s * min(l, 1 - l)
        let sv = v == 0 ? 0 : 2 * (1 - l / v)
        return UIColor(hue: CGFloat(hueDeg) / 360, saturation: sv, brightness: v, alpha: 1)
    }
}

// MARK: - Autocomplete Suggestion Popover

struct AutocompletePopover: View {
    let items: [String]
    let prefix: String
    let onSelect: (String) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Text("Suggestions")
                .font(.caption2).fontWeight(.bold).foregroundColor(.secondary)
                .padding(.horizontal, 12).padding(.vertical, 6)
            Divider()
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 8) {
                    ForEach(items, id: \.self) { item in
                        Button { onSelect(item) } label: {
                            HStack(spacing: 4) {
                                Image(systemName: prefix == "/" ? "command" : "person.crop.circle").font(.caption)
                                Text(item).font(.subheadline)
                            }
                            .padding(.horizontal, 10).padding(.vertical, 6)
                            .background(Color.blue.opacity(0.1)).cornerRadius(16)
                        }
                    }
                }
                .padding(.horizontal, 12).padding(.vertical, 8)
            }
        }
        .background(Color(uiColor: .secondarySystemBackground))
        .cornerRadius(12).padding(.horizontal, 12)
        .shadow(color: Color.black.opacity(0.05), radius: 4, x: 0, y: -2)
    }
}

// MARK: - Attachment Strip View

struct AttachmentStripView: View {
    @Binding var files: [URL]
    @Binding var images: [UIImage]

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 10) {
                ForEach(images.indices, id: \.self) { idx in
                    ZStack(alignment: .topTrailing) {
                        Image(uiImage: images[idx])
                            .resizable().scaledToFill()
                            .frame(width: 50, height: 50).cornerRadius(6).clipped()
                        Button { images.remove(at: idx) } label: {
                            Image(systemName: "xmark.circle.fill")
                                .foregroundColor(.gray).background(Circle().fill(Color.white))
                        }
                        .offset(x: 5, y: -5)
                    }
                }
                ForEach(files.indices, id: \.self) { idx in
                    ZStack(alignment: .topTrailing) {
                        VStack(spacing: 4) {
                            Image(systemName: "doc.fill").foregroundColor(.blue).font(.title3)
                            Text(files[idx].lastPathComponent).font(.caption2).lineLimit(1).frame(width: 45)
                        }
                        .padding(6).background(Color.gray.opacity(0.1)).cornerRadius(6)
                        Button { files.remove(at: idx) } label: {
                            Image(systemName: "xmark.circle.fill")
                                .foregroundColor(.gray).background(Circle().fill(Color.white))
                        }
                        .offset(x: 5, y: -5)
                    }
                }
            }
            .padding(.horizontal, 12).padding(.vertical, 8)
        }
        .background(Color(uiColor: .secondarySystemBackground))
    }
}
