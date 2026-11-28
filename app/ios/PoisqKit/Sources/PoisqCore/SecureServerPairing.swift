import CryptoKit
import Foundation

/// Public enrollment data acquired from a trusted poisq Studio QR code.
public struct SecureServerPairing: Equatable, Sendable {
    public let installationID: String
    /// Ed25519 root key of the installation; it signs working-key certificates and is pinned by the QR code.
    public let rootPublicKey: Data
    public let origin: URL

    /// Raw 16-byte installation ID.
    public var installationIDBytes: Data {
        Data(stride(from: 0, to: installationID.utf8.count, by: 2).map { offset in
            let start = installationID.index(installationID.startIndex, offsetBy: offset)
            return UInt8(installationID[start..<installationID.index(start, offsetBy: 2)], radix: 16)!
        })
    }

    /// Short root-key fingerprint for human comparison with poisq Studio: first 10 bytes of SHA-256 in groups of four hex digits.
    public var fingerprint: String {
        let hex = SHA256.hash(data: rootPublicKey).prefix(10).map { String(format: "%02x", $0) }.joined()
        return stride(from: 0, to: hex.count, by: 4).map { offset in
            let start = hex.index(hex.startIndex, offsetBy: offset)
            return String(hex[start..<hex.index(start, offsetBy: 4)])
        }.joined(separator: " ")
    }

    public init?(code: String) {
        guard code.utf8.count <= 512 else { return nil }

        let fields = code.trimmingCharacters(in: .whitespacesAndNewlines)
            .split(separator: ":", omittingEmptySubsequences: false)
        guard fields.count == 5, fields[0] == "poisq-secure", fields[1] == "v1" else { return nil }

        let id = fields[2]
        guard id.utf8.count == 32, id.utf8.allSatisfy({ (48...57).contains($0) || (97...102).contains($0) }),
              id.utf8.contains(where: { $0 != 48 }),
              let key = Self.decodeBase64URL(fields[3]), key.count == 32, key.contains(where: { $0 != 0 }),
              (try? Curve25519.Signing.PublicKey(rawRepresentation: key)) != nil,
              let address = Self.decodeBase64URL(fields[4]),
              let text = String(data: address, encoding: .utf8),
              let origin = Self.parseOrigin(text) else { return nil }

        installationID = String(id)
        rootPublicKey = key
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
