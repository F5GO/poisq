import CryptoKit
import Foundation
import Testing
@testable import PoisqCore

private struct NoiseVector: Decodable {
    struct Message: Decodable { let payload: String; let ciphertext: String }
    let init_prologue: String
    let init_ephemeral: String
    let init_remote_static: String
    let handshake_hash: String
    let messages: [Message]
}

private struct ChannelVector: Decodable {
    struct Call: Decodable { let counter: UInt64; let request: String; let body: String; let response: String; let records: String }
    let epoch: UInt32
    let clientEphemeral: String
    let hello: String
    let helloResponse: String
    let sessionId: String
    let sessionTtl: UInt32
    let maxRequest: UInt32
    let maxRecord: UInt32
    let calls: [Call]
}

private func key(_ hex: String) throws -> Curve25519.KeyAgreement.PrivateKey {
    try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: Data(hex: hex))
}

@Suite struct NoiseNKTests {
    @Test func matchesOfficialVector() throws {
        let v: NoiseVector = try loadSharedVector("noise-nk.json")
        var initiator = try NoiseNKInitiator(prologue: Data(hex: v.init_prologue), remoteStatic: Data(hex: v.init_remote_static),
                                             ephemeral: try key(v.init_ephemeral), payload: Data(hex: v.messages[0].payload))
        #expect(initiator.message1 == Data(hex: v.messages[0].ciphertext))
        let result = try initiator.readMessage2(Data(hex: v.messages[1].ciphertext))
        #expect(result.payload == Data(hex: v.messages[1].payload))
        #expect(result.handshakeHash == Data(hex: v.handshake_hash))
        var nonces: [UInt64] = [0, 0]
        for (i, m) in v.messages.dropFirst(2).enumerated() {
            let dir = i % 2
            let k = dir == 0 ? result.initiatorToResponder : result.responderToInitiator
            #expect(try NoiseCipher.seal(key: k, nonce: nonces[dir], ad: Data(), plaintext: Data(hex: m.payload)) == Data(hex: m.ciphertext))
            nonces[dir] += 1
        }
    }

    @Test func rejectsTamperedResponseAndLowOrderKeys() throws {
        let v: NoiseVector = try loadSharedVector("noise-nk.json")
        var initiator = try NoiseNKInitiator(prologue: Data(hex: v.init_prologue), remoteStatic: Data(hex: v.init_remote_static),
                                             ephemeral: try key(v.init_ephemeral), payload: Data())
        var bad = Data(hex: v.messages[1].ciphertext)
        bad[bad.count - 1] ^= 1
        #expect(throws: (any Error).self) { try initiator.readMessage2(bad) }
        #expect(throws: (any Error).self) {
            _ = try NoiseNKInitiator(prologue: Data(), remoteStatic: Data(count: 32), ephemeral: .init(), payload: Data())
        }
    }
}

@Suite struct SecureChannelWireTests {
    @Test func matchesGoChannelVector() throws {
        let v: ChannelVector = try loadSharedVector("channel-v1.json")
        let pv = try SecurePairingVector.load()
        let pairing = try #require(SecureServerPairing(code: pv.pairing.code))
        let cert = try #require(SecureWorkingCertificate(data: Data(hex: pv.certificate.hex), pairing: pairing))

        var initiator = try NoiseNKInitiator(prologue: SecureChannelWire.prologue(installationID: pairing.installationIDBytes, epoch: v.epoch),
                                             remoteStatic: cert.workingKey, ephemeral: try key(v.clientEphemeral), payload: Data())
        #expect(SecureChannelWire.helloBody(epoch: v.epoch, message1: initiator.message1) == Data(hex: v.hello))
        let result = try initiator.readMessage2(Data(hex: v.helloResponse))
        let params = try SecureChannelWire.parseHelloPayload(result.payload)
        #expect(params == .init(id: Data(hex: v.sessionId), ttl: v.sessionTtl, maxRequest: v.maxRequest, maxRecord: v.maxRecord))

        for call in v.calls {
            let body = try SecureChannelWire.sealCall(sessionID: params.id, c2s: result.initiatorToResponder, counter: call.counter, request: Data(hex: call.request))
            #expect(body == Data(hex: call.body))
            let header = body.prefix(SecureChannelWire.callHeaderSize)
            let records = Data(hex: call.records)
            let response = try SecureChannelWire.openRecords(records, s2c: result.responderToInitiator, header: header, counter: call.counter,
                                                             maxRecord: Int(params.maxRecord), limit: 1 << 20)
            #expect(response == Data(hex: call.response))

            // Any flipped byte, truncation, trailing data or another counter breaks the response.
            for index in stride(from: 0, to: records.count, by: max(1, records.count / 40)) {
                var bad = records
                bad[index] ^= 1
                #expect(throws: (any Error).self) {
                    try SecureChannelWire.openRecords(bad, s2c: result.responderToInitiator, header: header, counter: call.counter, maxRecord: Int(params.maxRecord), limit: 1 << 20)
                }
            }
            #expect(throws: (any Error).self) {
                try SecureChannelWire.openRecords(records.dropLast(), s2c: result.responderToInitiator, header: header, counter: call.counter, maxRecord: Int(params.maxRecord), limit: 1 << 20)
            }
            #expect(throws: (any Error).self) {
                try SecureChannelWire.openRecords(records + Data([0]), s2c: result.responderToInitiator, header: header, counter: call.counter, maxRecord: Int(params.maxRecord), limit: 1 << 20)
            }
            #expect(throws: (any Error).self) {
                try SecureChannelWire.openRecords(records, s2c: result.responderToInitiator, header: header, counter: call.counter + 1, maxRecord: Int(params.maxRecord), limit: 1 << 20)
            }
        }
    }
}

/// Fake Studio built from the Go vector: answers with recorded bytes and checks what the client sends.
private final class VectorTransport: SecureChannelTransport, @unchecked Sendable {
    let lock = NSLock()
    var script: [(path: String, status: Int, body: Data, expect: Data?)]
    var sent: [String] = []

    init(_ script: [(String, Int, Data, Data?)]) { self.script = script.map { ($0.0, $0.1, $0.2, $0.3) } }

    func send(_ request: URLRequest) async throws -> (Data, Int) {
        try lock.withLock {
            let path = request.url!.path
            sent.append(path)
            guard !script.isEmpty else { throw URLError(.badServerResponse) }
            let step = script.removeFirst()
            guard path == "/poisq-secure/v1/" + step.path else { throw URLError(.unsupportedURL) }
            if let expect = step.expect, request.httpBody != expect { throw URLError(.cannotParseResponse) }
            return (step.body, step.status)
        }
    }
}

@Suite struct SecureChannelTests {
    private struct Fixture {
        let pairing: SecureServerPairing
        let certificate: Data
        let vector: ChannelVector
        let ephemeral: Data
    }

    private func fixture() throws -> Fixture {
        let pv = try SecurePairingVector.load()
        let v: ChannelVector = try loadSharedVector("channel-v1.json")
        return Fixture(pairing: try #require(SecureServerPairing(code: pv.pairing.code)), certificate: Data(hex: pv.certificate.hex),
                       vector: v, ephemeral: Data(hex: v.clientEphemeral))
    }

    private func channel(_ f: Fixture, _ transport: VectorTransport, certificate: Data? = nil, acceptedEpoch: UInt32? = nil) -> SecureChannel {
        let eph = f.ephemeral
        return SecureChannel(pairing: f.pairing, certificate: certificate, acceptedEpoch: acceptedEpoch, transport: transport,
                             now: { Date(timeIntervalSince1970: 1_767_225_600 + 3600) },
                             makeEphemeral: { try! Curve25519.KeyAgreement.PrivateKey(rawRepresentation: eph) })
    }

    @Test func fetchesCertificateHandshakesAndCalls() async throws {
        let f = try fixture()
        let call = f.vector.calls[0]
        let transport = VectorTransport([
            ("certificate", 200, f.certificate, nil),
            ("hello", 200, Data(hex: f.vector.helloResponse), Data(hex: f.vector.hello)),
            ("call", 200, Data(hex: call.records), Data(hex: call.body)),
        ])
        let ch = channel(f, transport)
        #expect(try await ch.call(Data(hex: call.request)) == Data(hex: call.response))
        #expect(await ch.acceptedEpoch == f.vector.epoch)
        #expect(await ch.hasValidSession)
    }

    @Test func rehandshakesOnUnknownSession() async throws {
        let f = try fixture()
        let call = f.vector.calls[0]
        let transport = VectorTransport([
            ("hello", 200, Data(hex: f.vector.helloResponse), Data(hex: f.vector.hello)),
            ("call", 410, Data(), Data(hex: call.body)),
            ("hello", 200, Data(hex: f.vector.helloResponse), Data(hex: f.vector.hello)),
            ("call", 200, Data(hex: call.records), Data(hex: call.body)),
        ])
        let ch = channel(f, transport, certificate: f.certificate)
        #expect(try await ch.call(Data(hex: call.request)) == Data(hex: call.response))
    }

    @Test func followsWorkingKeyRotation() async throws {
        let f = try fixture()
        let call = f.vector.calls[0]
        let transport = VectorTransport([
            ("hello", 409, f.certificate, Data(hex: f.vector.hello)),
            ("hello", 200, Data(hex: f.vector.helloResponse), Data(hex: f.vector.hello)),
            ("call", 200, Data(hex: call.records), Data(hex: call.body)),
        ])
        let ch = channel(f, transport, certificate: f.certificate)
        #expect(try await ch.call(Data(hex: call.request)) == Data(hex: call.response))
    }

    @Test func refusesUntrustedOrRolledBackCertificates() async throws {
        let f = try fixture()
        var forged = f.certificate
        forged[forged.count - 1] ^= 1
        let forgedTransport = VectorTransport([("certificate", 200, forged, nil)])
        await #expect(throws: SecureChannel.Failure.untrustedCertificate) { try await channel(f, forgedTransport).call(Data([1])) }
        #expect(forgedTransport.sent == ["/poisq-secure/v1/certificate"], "nothing may be sent after a bad certificate")

        let rollback = VectorTransport([("certificate", 200, f.certificate, nil)])
        await #expect(throws: SecureChannel.Failure.untrustedCertificate) {
            try await channel(f, rollback, acceptedEpoch: f.vector.epoch + 1).call(Data([1]))
        }

        let rotatedForged = VectorTransport([("hello", 409, forged, Data(hex: f.vector.hello))])
        await #expect(throws: SecureChannel.Failure.untrustedCertificate) {
            try await channel(f, rotatedForged, certificate: f.certificate).call(Data([1]))
        }
    }

    @Test func mapsServerRefusals() async throws {
        let f = try fixture()
        for (status, failure) in [(404, SecureChannel.Failure.disabled), (429, .rateLimited), (302, .http(302))] {
            let transport = VectorTransport([("hello", status, Data(), nil)])
            await #expect(throws: failure) { try await channel(f, transport, certificate: f.certificate).call(Data([1])) }
            #expect(transport.sent.count == 1)
        }
        let tampered = VectorTransport([
            ("hello", 200, Data(hex: f.vector.helloResponse), nil),
            ("call", 200, Data(hex: f.vector.calls[0].records).dropLast(), nil),
        ])
        await #expect(throws: SecureChannel.Failure.malformedResponse) {
            try await channel(f, tampered, certificate: f.certificate).call(Data(hex: f.vector.calls[0].request))
        }
    }

    @Test func rejectsOversizedRequestBeforeSending() async throws {
        let f = try fixture()
        let transport = VectorTransport([("hello", 200, Data(hex: f.vector.helloResponse), nil)])
        await #expect(throws: SecureChannel.Failure.requestTooLarge) {
            try await channel(f, transport, certificate: f.certificate).call(Data(count: Int(f.vector.maxRequest) + 1))
        }
        #expect(transport.sent == ["/poisq-secure/v1/hello"])
    }
}

/// Records counters of concurrent calls; refuses them so no response has to be forged.
private final class CounterTransport: SecureChannelTransport, @unchecked Sendable {
    let lock = NSLock()
    let helloResponse: Data
    var counters: [UInt64] = []
    init(helloResponse: Data) { self.helloResponse = helloResponse }

    func send(_ request: URLRequest) async throws -> (Data, Int) {
        try await Task.sleep(for: .milliseconds(Int.random(in: 0...3)))
        return lock.withLock {
            if request.url!.path.hasSuffix("hello") { return (helloResponse, 200) }
            let header = [UInt8](request.httpBody!.prefix(25))
            counters.append(header[17..<25].reduce(UInt64(0)) { $0 << 8 | UInt64($1) })
            return (Data(), 500)
        }
    }
}

@Suite struct SecureChannelConcurrencyTests {
    @Test func concurrentCallsShareOneHandshakeAndNeverReuseCounters() async throws {
        let pv = try SecurePairingVector.load()
        let v: ChannelVector = try loadSharedVector("channel-v1.json")
        let pairing = try #require(SecureServerPairing(code: pv.pairing.code))
        let transport = CounterTransport(helloResponse: Data(hex: v.helloResponse))
        let eph = Data(hex: v.clientEphemeral)
        let ch = SecureChannel(pairing: pairing, certificate: Data(hex: pv.certificate.hex), acceptedEpoch: nil, transport: transport,
                               now: { Date(timeIntervalSince1970: 1_767_225_600 + 3600) },
                               makeEphemeral: { try! Curve25519.KeyAgreement.PrivateKey(rawRepresentation: eph) })
        await withTaskGroup(of: Void.self) { group in
            for _ in 0..<64 { group.addTask { _ = try? await ch.call(Data([1])) } }
        }
        let counters = transport.lock.withLock { transport.counters }
        #expect(counters.count == 64)
        #expect(Set(counters).count == 64, "a counter was used twice")
        #expect(Set(counters) == Set(1...64))
    }
}

/// Talks to a real local Studio; runs only when POISQ_INTEROP_URL and POISQ_INTEROP_CODE are set.
private final class RewritingTransport: SecureChannelTransport, @unchecked Sendable {
    let base: URL
    let inner = URLSessionSecureTransport()
    init(base: URL) { self.base = base }
    func send(_ request: URLRequest) async throws -> (Data, Int) {
        var r = request
        r.url = base.appending(path: String(request.url!.path.dropFirst()))
        return try await inner.send(r)
    }
}

@Suite(.enabled(if: ProcessInfo.processInfo.environment["POISQ_INTEROP_URL"] != nil))
struct SecureChannelInteropTests {
    @Test func talksToLocalStudio() async throws {
        let env = ProcessInfo.processInfo.environment
        let code = try #require(env["POISQ_INTEROP_CODE"])
        let pairing = try #require(SecureServerPairing(code: code))
        let base = try #require(URL(string: env["POISQ_INTEROP_URL"] ?? ""))
        let ch = SecureChannel(pairing: pairing, certificate: nil, acceptedEpoch: nil,
                               transport: RewritingTransport(base: base),
                               now: { Date() }, makeEphemeral: { .init() })
        try await ch.verify()
        #expect(await ch.acceptedEpoch != nil)
        let big = Data((0..<60_000).map { UInt8($0 % 256) })
        #expect(try await ch.call(Data([1]) + big) == Data([0]) + big, "multi-record echo")
        try await withThrowingTaskGroup(of: Void.self) { group in
            for _ in 0..<16 { group.addTask { try await ch.verify() } }
            try await group.waitForAll()
        }
        #expect(try await ch.call(Data([0x7f])) == Data([0x01]))
    }
}
