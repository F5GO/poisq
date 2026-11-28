import CryptoKit
import Foundation

/// Initiator side of `Noise_NK_25519_AESGCM_SHA256` (Noise rev. 34), matching Go `securetransport/noise.go`:
///
///     <- s
///     ...
///     -> e, es
///     <- e, ee
///
/// Checked against the official Cacophony vector (internal/securetransport/testdata/noise-nk.json).
struct NoiseNKInitiator {
    enum Failure: Error { case invalidKey, decryptFailed }

    struct Result {
        let payload: Data
        let initiatorToResponder: Data
        let responderToInitiator: Data
        let handshakeHash: Data
    }

    private var state: NoiseSymmetricState
    private let ephemeral: Curve25519.KeyAgreement.PrivateKey
    /// First handshake message to send.
    let message1: Data

    init(prologue: Data, remoteStatic: Data, ephemeral: Curve25519.KeyAgreement.PrivateKey, payload: Data) throws {
        var state = NoiseSymmetricState(prologue: prologue)
        state.mixHash(remoteStatic)
        let e = ephemeral.publicKey.rawRepresentation
        state.mixHash(e)
        try state.mixKey(Self.dh(ephemeral, remoteStatic))
        message1 = e + (try state.encryptAndHash(payload))
        self.state = state
        self.ephemeral = ephemeral
    }

    mutating func readMessage2(_ message: Data) throws -> Result {
        guard message.count >= 32 + 16 else { throw Failure.decryptFailed }
        let bytes = Data(message)
        let re = bytes.prefix(32)
        state.mixHash(re)
        try state.mixKey(Self.dh(ephemeral, re))
        let payload = try state.decryptAndHash(bytes.dropFirst(32))
        let (c2s, s2c) = state.split()
        return Result(payload: payload, initiatorToResponder: c2s, responderToInitiator: s2c, handshakeHash: state.h)
    }

    /// X25519 with an explicit check for low-order points: an all-zero shared secret is rejected.
    private static func dh(_ privateKey: Curve25519.KeyAgreement.PrivateKey, _ publicKey: Data) throws -> Data {
        guard let peer = try? Curve25519.KeyAgreement.PublicKey(rawRepresentation: publicKey),
              let secret = try? privateKey.sharedSecretFromKeyAgreement(with: peer) else { throw Failure.invalidKey }
        let raw = secret.withUnsafeBytes { Data($0) }
        guard raw.contains(where: { $0 != 0 }) else { throw Failure.invalidKey }
        return raw
    }
}

struct NoiseSymmetricState {
    private static let protocolName = Data("Noise_NK_25519_AESGCM_SHA256".utf8)

    private(set) var ck: Data
    private(set) var h: Data
    private var k: Data?
    private var n: UInt64 = 0

    init(prologue: Data) {
        // The protocol name is shorter than HASHLEN, so it is zero-padded rather than hashed.
        var h = Self.protocolName
        h.append(Data(count: 32 - h.count))
        self.h = h
        ck = h
        mixHash(prologue)
    }

    mutating func mixHash(_ data: Data) {
        var hasher = SHA256()
        hasher.update(data: h)
        hasher.update(data: data)
        h = Data(hasher.finalize())
    }

    mutating func mixKey(_ ikm: Data) throws {
        (ck, k) = Self.hkdf(chainingKey: ck, ikm: ikm)
        n = 0
    }

    mutating func encryptAndHash(_ plaintext: Data) throws -> Data {
        guard let k else {
            mixHash(plaintext)
            return plaintext
        }
        let c = try NoiseCipher.seal(key: k, nonce: n, ad: h, plaintext: plaintext)
        n += 1
        mixHash(c)
        return c
    }

    mutating func decryptAndHash(_ ciphertext: Data) throws -> Data {
        guard let k else {
            mixHash(ciphertext)
            return ciphertext
        }
        guard let p = try? NoiseCipher.open(key: k, nonce: n, ad: h, ciphertext: ciphertext) else {
            throw NoiseNKInitiator.Failure.decryptFailed
        }
        n += 1
        mixHash(ciphertext)
        return p
    }

    func split() -> (Data, Data) { Self.hkdf(chainingKey: ck, ikm: Data()) }

    static func hkdf(chainingKey: Data, ikm: Data) -> (Data, Data) {
        let temp = hmac(chainingKey, ikm)
        let out1 = hmac(temp, Data([0x01]))
        let out2 = hmac(temp, out1 + Data([0x02]))
        return (out1, out2)
    }

    static func hmac(_ key: Data, _ data: Data) -> Data {
        Data(HMAC<SHA256>.authenticationCode(for: data, using: SymmetricKey(data: key)))
    }
}

/// AES-256-GCM with the Noise nonce layout: 32 zero bits followed by a 64-bit big-endian counter.
enum NoiseCipher {
    static func nonce(_ n: UInt64) -> AES.GCM.Nonce {
        var bytes = Data(count: 4)
        withUnsafeBytes(of: n.bigEndian) { bytes.append(contentsOf: $0) }
        return try! AES.GCM.Nonce(data: bytes)
    }

    static func seal(key: Data, nonce n: UInt64, ad: Data, plaintext: Data) throws -> Data {
        let box = try AES.GCM.seal(plaintext, using: SymmetricKey(data: key), nonce: nonce(n), authenticating: ad)
        return box.ciphertext + box.tag
    }

    static func open(key: Data, nonce n: UInt64, ad: Data, ciphertext: Data) throws -> Data {
        guard ciphertext.count >= 16 else { throw NoiseNKInitiator.Failure.decryptFailed }
        let bytes = Data(ciphertext)
        let box = try AES.GCM.SealedBox(nonce: nonce(n), ciphertext: bytes.dropLast(16), tag: bytes.suffix(16))
        return try AES.GCM.open(box, using: SymmetricKey(data: key), authenticating: ad)
    }
}
