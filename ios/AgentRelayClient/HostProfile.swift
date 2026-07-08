import Foundation
import SwiftData

@Model
public final class HostProfile {
    @Attribute(.unique) public var id: UUID
    public var name: String
    public var hostUrlString: String
    public var apiToken: String
    public var isDefault: Bool
    public var createdAt: Date
    
    public var url: URL? {
        URL(string: hostUrlString)
    }
    
    public init(id: UUID = UUID(), name: String, hostUrlString: String, apiToken: String = "", isDefault: Bool = false, createdAt: Date = Date()) {
        self.id = id
        self.name = name
        self.hostUrlString = hostUrlString
        self.apiToken = apiToken
        self.isDefault = isDefault
        self.createdAt = createdAt
    }
}
