import CryptoKit
import Foundation
import Testing
@testable import PoisqCore

private struct BlobVector: Decodable {
    let key: String
    let size: Int
    let plainSha256: String
    let cipherBase64: String
}

@Suite struct SecureBlobTests {
    private func temp(_ name: String) -> URL {
        FileManager.default.temporaryDirectory.appending(path: "poisq-blob-\(UUID().uuidString)-\(name)")
    }

    @Test func decryptsGoBlob() throws {
        let v: BlobVector = try loadSharedVector("blob-v1.json")
        let cipher = try #require(Data(base64Encoded: v.cipherBase64))
        let src = temp("in"), dst = temp("out")
        defer { try? FileManager.default.removeItem(at: src); try? FileManager.default.removeItem(at: dst) }
        try cipher.write(to: src)
        try SecureBlob.decrypt(from: src, to: dst, key: Data(hex: v.key), size: v.size, chunkSize: 65536)
        let plain = try Data(contentsOf: dst)
        #expect(plain.count == v.size)
        #expect(SHA256.hash(data: plain).map { String(format: "%02x", $0) }.joined() == v.plainSha256)
    }

    @Test func rejectsTamperedTruncatedOrForeignBlobs() throws {
        let v: BlobVector = try loadSharedVector("blob-v1.json")
        let cipher = try #require(Data(base64Encoded: v.cipherBase64))
        let key = Data(hex: v.key)
        func attempt(_ data: Data, key: Data, size: Int) -> SecureBlob.Failure? {
            let src = temp("in"), dst = temp("out")
            defer { try? FileManager.default.removeItem(at: src) }
            try? data.write(to: src)
            do { try SecureBlob.decrypt(from: src, to: dst, key: key, size: size, chunkSize: 65536); return nil }
            catch {
                #expect(!FileManager.default.fileExists(atPath: dst.path), "a failed decryption must not leave a file")
                return error as? SecureBlob.Failure
            }
        }
        for index in [0, 100, 65_551, 65_552, cipher.count - 1] {
            var bad = cipher
            bad[index] ^= 1
            #expect(attempt(bad, key: key, size: v.size) == .authentication)
        }
        #expect(attempt(cipher.dropLast(), key: key, size: v.size) == .size)
        #expect(attempt(cipher + Data([0]), key: key, size: v.size) == .size)
        var otherKey = key
        otherKey[0] ^= 1
        #expect(attempt(cipher, key: otherKey, size: v.size) == .authentication)
        // Перестановка кусков ломает AAD (индекс и признак последнего куска).
        let first = cipher.prefix(65552), second = cipher.dropFirst(65552)
        #expect(attempt(Data(second) + Data(first), key: key, size: v.size) != nil)
    }
}
