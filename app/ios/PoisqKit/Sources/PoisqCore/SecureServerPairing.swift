import Foundation

/// Public enrollment data acquired from a trusted poisq Studio QR code.
public struct SecureServerPairing: Equatable, Sendable {
    public let installationID: String
    public let publicKey: Data
    public let origin: URL

    public init?(code: String) {
        guard code.utf8.count <= 512 else { return nil }

        let fields = code.trimmingCharacters(in: .whitespacesAndNewlines)
            .split(separator: ":", omittingEmptySubsequences: false)
        guard fields.count == 5, fields[0] == "poisq-secure", fields[1] == "v1" else { return nil }

        let id = fields[2]
        guard id.utf8.count == 32, id.utf8.allSatisfy({ (48...57).contains($0) || (97...102).contains($0) }),
              id.utf8.contains(where: { $0 != 48 }),
              let key = Self.decodeBase64URL(fields[3]), key.count == 32, key.contains(where: { $0 != 0 }),
              let address = Self.decodeBase64URL(fields[4]),
              let text = String(data: address, encoding: .utf8),
              let origin = Self.parseOrigin(text) else { return nil }

        installationID = String(id)
        publicKey = key
        self.origin = origin
    }

    private static func decodeBase64URL(_ text: Substring) -> Data? {
        let base64 = text.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/")
        let padding = String(repeating: "=", count: (4 - base64.utf8.count % 4) % 4)
        guard let bytes = Data(base64Encoded: base64 + padding) else { return nil }
        let canonical = bytes.base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
        return text == canonical ? bytes : nil
    }

    private static func parseOrigin(_ text: String) -> URL? {
        guard let components = URLComponents(string: text), let host = components.host,
              validHostname(host) else { return nil }

        var canonical = "https://" + host
        if let port = components.port {
            guard (1...65535).contains(port), port != 443 else { return nil }
            canonical += ":\(port)"
        }
        guard text == canonical else { return nil }
        return components.url
    }

    private static func validHostname(_ host: String) -> Bool {
        guard !host.isEmpty, host.utf8.count <= 253 else { return false }
        return host.split(separator: ".", omittingEmptySubsequences: false).allSatisfy { label in
            (1...63).contains(label.utf8.count) && label.first != "-" && label.last != "-" &&
                label.utf8.allSatisfy { (97...122).contains($0) || (48...57).contains($0) || $0 == 45 }
        }
    }
}
