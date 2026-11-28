import CryptoKit
import Foundation

/// Wire format of the poisq secure channel v1, matching Go `securetransport/channel.go` byte for byte
/// (checked by internal/securetransport/testdata/channel-v1.json).
///
///     hello:  version u8 | epoch u32 | noise message 1                      (53 bytes)
///     reply:  noise message 2 with session_id [16] | ttl u32 | maxRequest u32 | maxRecord u32
///     call:   version u8 | session_id [16] | counter u64 | AES-GCM(request)
///     reply:  records: frame u32 (low 31 bits — ciphertext length, high bit — final) | AES-GCM(chunk)
enum SecureChannelWire {
    enum Failure: Error { case malformed }

    static let version: UInt8 = 1
    static let pathPrefix = "poisq-secure/v1/"
    static let helloSize = 53
    static let callHeaderSize = 25
    static let finalBit: UInt32 = 1 << 31

    struct SessionParameters: Equatable {
        let id: Data
        let ttl: UInt32
        let maxRequest: UInt32
        let maxRecord: UInt32
    }

    static func prologue(installationID: Data, epoch: UInt32) -> Data {
        Data("poisq-secure/v1".utf8) + installationID + bigEndian(epoch)
    }

    static func helloBody(epoch: UInt32, message1: Data) -> Data {
        Data([version]) + bigEndian(epoch) + message1
    }

    static func parseHelloPayload(_ payload: Data) throws -> SessionParameters {
        let b = [UInt8](payload)
        guard b.count == 28 else { throw Failure.malformed }
        let p = SessionParameters(id: Data(b[0..<16]), ttl: uint32(b[16..<20]), maxRequest: uint32(b[20..<24]), maxRecord: uint32(b[24..<28]))
        // Bounds protect the client from a misbehaving server; the real values are far below them.
        guard p.ttl >= 10, (1...(1 << 20)).contains(p.maxRequest), (1...(1 << 20)).contains(p.maxRecord) else { throw Failure.malformed }
        return p
    }

    static func deriveKey(root: Data, label: String, counter: UInt64) -> Data {
        NoiseSymmetricState.hmac(root, Data(label.utf8) + bigEndian(counter))
    }

    static func callHeader(sessionID: Data, counter: UInt64) -> Data {
        Data([version]) + sessionID + bigEndian(counter)
    }

    static func recordAAD(header: Data, index: UInt64, final: Bool) -> Data {
        header + bigEndian(index) + Data([final ? 1 : 0])
    }

    static func sealCall(sessionID: Data, c2s: Data, counter: UInt64, request: Data) throws -> Data {
        let header = callHeader(sessionID: sessionID, counter: counter)
        let key = deriveKey(root: c2s, label: "poisq-secure/v1 request", counter: counter)
        return header + (try NoiseCipher.seal(key: key, nonce: 0, ad: recordAAD(header: header, index: 0, final: true), plaintext: request))
    }

    /// Decrypts a complete response, checking record order, size, the final record and the total `limit`.
    static func openRecords(_ data: Data, s2c: Data, header: Data, counter: UInt64, maxRecord: Int, limit: Int) throws -> Data {
        let key = deriveKey(root: s2c, label: "poisq-secure/v1 response", counter: counter)
        let bytes = [UInt8](data)
        var offset = 0
        var out = Data()
        var index: UInt64 = 0
        while true {
            guard bytes.count - offset >= 4 else { throw Failure.malformed }
            let frame = uint32(bytes[offset..<offset + 4])
            let final = frame & finalBit != 0
            let length = Int(frame & ~finalBit)
            offset += 4
            guard length >= 16, length <= maxRecord + 16, bytes.count - offset >= length else { throw Failure.malformed }
            let chunk = try NoiseCipher.open(key: key, nonce: index, ad: recordAAD(header: header, index: index, final: final),
                                             ciphertext: Data(bytes[offset..<offset + length]))
            offset += length
            guard out.count + chunk.count <= limit else { throw Failure.malformed }
            out.append(chunk)
            if final {
                guard offset == bytes.count else { throw Failure.malformed }
                return out
            }
            index += 1
        }
    }

    static func bigEndian<T: FixedWidthInteger>(_ value: T) -> Data {
        withUnsafeBytes(of: value.bigEndian) { Data($0) }
    }

    static func uint32(_ slice: ArraySlice<UInt8>) -> UInt32 {
        slice.reduce(0) { $0 << 8 | UInt32($1) }
    }
}
