import CryptoKit
import Foundation

/// Working-key certificate v1 signed by the installation root key (format shared with Go `securetransport.WorkingCertificate`):
///
///     version u8 | installation_id [16] | epoch u32 BE | not_before i64 BE | not_after i64 BE |
///     working_key [32] (X25519) | next_root_digest [32] (SHA-256) | signature [64]
///
/// The signature covers `context || everything before the signature`.
public struct SecureWorkingCertificate: Equatable, Sendable {
    public static let size = 165
    /// Bounds the damage of a mis-issued or stolen working key.
    public static let maxLifetime: TimeInterval = 8 * 24 * 3600
    /// Tolerance for the phone clock when checking validity dates.
    public static let clockSkew: TimeInterval = 3600

    private static let bodySize = 101
    private static let context = Data("poisq-secure/v1 working-key certificate".utf8)

    public let epoch: UInt32
    public let notBefore: Date
    public let notAfter: Date
    /// X25519 static key of Studio for this epoch.
    public let workingKey: Data
    /// SHA-256 commitment to the next root key.
    public let nextRootDigest: Data
    /// Exact bytes as received; cached and re-verified on load.
    public let rawValue: Data

    /// Verifies structure, installation and signature against the root key pinned by `pairing`. Dates are checked by `isAcceptable`.
    public init?(data: Data, pairing: SecureServerPairing) {
        let bytes = [UInt8](data)
        guard bytes.count == Self.size, bytes[0] == 1,
              let root = try? Curve25519.Signing.PublicKey(rawRepresentation: pairing.rootPublicKey) else { return nil }
        let body = Data(bytes[0..<Self.bodySize])
        guard root.isValidSignature(Data(bytes[Self.bodySize...]), for: Self.context + body),
              Data(bytes[1..<17]) == pairing.installationIDBytes else { return nil }

        let epoch = Self.uint(bytes[17..<21])
        let notBefore = Int64(bitPattern: Self.uint(bytes[21..<29]))
        let notAfter = Int64(bitPattern: Self.uint(bytes[29..<37]))
        let workingKey = Data(bytes[37..<69])
        let nextRootDigest = Data(bytes[69..<101])
        guard epoch != 0, notBefore > 0, notAfter > notBefore,
              Double(notAfter - notBefore) <= Self.maxLifetime,
              workingKey.contains(where: { $0 != 0 }), nextRootDigest.contains(where: { $0 != 0 }) else { return nil }

        self.epoch = UInt32(epoch)
        self.notBefore = Date(timeIntervalSince1970: TimeInterval(notBefore))
        self.notAfter = Date(timeIntervalSince1970: TimeInterval(notAfter))
        self.workingKey = workingKey
        self.nextRootDigest = nextRootDigest
        rawValue = data
    }

    /// Whether the certificate is in force at `date`, allowing `clockSkew` for the phone clock.
    public func isValid(at date: Date) -> Bool {
        date.addingTimeInterval(Self.clockSkew) >= notBefore && date.addingTimeInterval(-Self.clockSkew) < notAfter
    }

    /// Accepts the certificate only if it is in force and not older than the highest epoch this profile already accepted.
    public func isAcceptable(afterAcceptedEpoch accepted: UInt32?, at date: Date) -> Bool {
        epoch >= (accepted ?? 0) && isValid(at: date)
    }

    private static func uint(_ slice: ArraySlice<UInt8>) -> UInt64 {
        slice.reduce(0) { $0 << 8 | UInt64($1) }
    }
}
