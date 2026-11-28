import Foundation

/// Shared Go↔Swift vector: internal/securetransport/testdata/pairing-v1.json is the single source of truth.
struct SecurePairingVector: Decodable {
    struct Pairing: Decodable {
        let code: String
        let installationId: String
        let rootPublicKey: String
        let origin: String
        let fingerprint: String
    }
    struct Certificate: Decodable {
        let hex: String
        let epoch: UInt32
        let notBefore: Int64
        let notAfter: Int64
        let workingKey: String
        let nextRootDigest: String
    }
    let pairing: Pairing
    let certificate: Certificate

    static func load() throws -> SecurePairingVector { try loadSharedVector("pairing-v1.json") }
}

/// Reads a shared Go↔Swift vector from internal/securetransport/testdata.
func loadSharedVector<T: Decodable>(_ name: String) throws -> T {
    // Tests/PoisqCoreTests → PoisqKit → ios → app → repository root.
    let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent().deletingLastPathComponent()
    return try JSONDecoder().decode(T.self, from: Data(contentsOf: root.appending(path: "internal/securetransport/testdata/" + name)))
}

extension Data {
    init(hex: String) {
        self.init(stride(from: 0, to: hex.count, by: 2).map { offset in
            let start = hex.index(hex.startIndex, offsetBy: offset)
            return UInt8(hex[start..<hex.index(start, offsetBy: 2)], radix: 16)!
        })
    }
}
