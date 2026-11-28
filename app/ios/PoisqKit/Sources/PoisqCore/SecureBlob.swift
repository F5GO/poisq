import CryptoKit
import Foundation

/// Билет офлайн-загрузки: ссылка без секретов и ключ файла, полученный только внутри канала.
public struct SecureDownloadTicket: Codable, Equatable, Sendable {
    public let url: URL
    public let key: Data
    public let size: Int
    public let chunkSize: Int

    public init(url: URL, key: Data, size: Int, chunkSize: Int) {
        self.url = url; self.key = key; self.size = size; self.chunkSize = chunkSize
    }
}

/// Расшифровка офлайн-загрузки (формат Go `securetransport/blob.go`, вектор testdata/blob-v1.json).
/// Кусок i: AES-256-GCM, nonce — 32 нулевых бита и i, AAD — метка ‖ i ‖ число кусков ‖ признак последнего.
public enum SecureBlob {
    public enum Failure: Error, Equatable { case size, authentication, io }

    private static let label = Data("poisq-secure/v1 blob".utf8)

    public static func chunkCount(size: Int, chunkSize: Int) -> Int { size <= 0 ? 1 : (size + chunkSize - 1) / chunkSize }

    public static func cipherSize(size: Int, chunkSize: Int) -> Int { size + 16 * chunkCount(size: size, chunkSize: chunkSize) }

    /// Расшифровывает `source` в `destination` потоком, кусок за куском; при любой ошибке `destination` удаляется.
    public static func decrypt(from source: URL, to destination: URL, key: Data, size: Int, chunkSize: Int) throws {
        guard key.count == 32, chunkSize > 0, size >= 0 else { throw Failure.size }
        let attributes = try? FileManager.default.attributesOfItem(atPath: source.path)
        guard (attributes?[.size] as? NSNumber)?.intValue == cipherSize(size: size, chunkSize: chunkSize) else { throw Failure.size }
        guard let input = try? FileHandle(forReadingFrom: source) else { throw Failure.io }
        defer { try? input.close() }
        FileManager.default.createFile(atPath: destination.path, contents: nil)
        guard let output = try? FileHandle(forWritingTo: destination) else { throw Failure.io }
        do {
            defer { try? output.close() }
            let total = chunkCount(size: size, chunkSize: chunkSize)
            var remaining = size
            for i in 0..<total {
                let plainLength = min(chunkSize, remaining)
                guard let chunk = try input.read(upToCount: plainLength + 16), chunk.count == plainLength + 16 else { throw Failure.size }
                let aad = label + SecureChannelWire.bigEndian(UInt64(i)) + SecureChannelWire.bigEndian(UInt64(total)) + Data([i == total - 1 ? 1 : 0])
                guard let plain = try? NoiseCipher.open(key: key, nonce: UInt64(i), ad: aad, ciphertext: chunk) else { throw Failure.authentication }
                try output.write(contentsOf: plain)
                remaining -= plainLength
            }
            guard try input.read(upToCount: 1)?.isEmpty ?? true else { throw Failure.size }
        } catch {
            try? FileManager.default.removeItem(at: destination)
            throw (error as? Failure) ?? Failure.io
        }
    }
}

extension SecureChannel {
    /// Билет на офлайн-загрузку трека: Navidrome проверяет право пользователя, Studio выдаёт ссылку и ключ.
    public func downloadTicket(songID: String) async throws -> SecureDownloadTicket {
        let id = Data(songID.utf8)
        guard !id.isEmpty, id.count <= 255 else { throw Failure.invalidInput }
        let reply = [UInt8](try await request(Data([0x12, UInt8(id.count)]) + id, responseLimit: 4096))
        guard let status = reply.first else { throw Failure.malformedResponse }
        switch status {
        case 0x00: break
        case 0x02: throw Failure.forbidden
        case 0x09: throw Failure.rangeUnsupported
        case 0x06: throw Failure.serverUnavailable
        default: throw Failure.malformedResponse
        }
        guard reply.count >= 3 else { throw Failure.malformedResponse }
        let n = Int(reply[1]) << 8 | Int(reply[2])
        guard reply.count == 3 + n + 32 + 8 + 4, let token = String(bytes: reply[3..<3 + n], encoding: .ascii),
              token.allSatisfy({ $0.isLetter || $0.isNumber || $0 == "-" || $0 == "_" }) else { throw Failure.malformedResponse }
        let rest = Array(reply[(3 + n)...])
        let size = rest[32..<40].reduce(UInt64(0)) { $0 << 8 | UInt64($1) }
        let chunk = SecureChannelWire.uint32(rest[40..<44][...])
        guard size < 1 << 40, chunk > 0, chunk <= 1 << 20 else { throw Failure.malformedResponse }
        return SecureDownloadTicket(url: pairing.origin.appending(path: SecureChannelWire.pathPrefix + "blob/" + token),
                                    key: Data(rest[0..<32]), size: Int(size), chunkSize: Int(chunk))
    }
}
