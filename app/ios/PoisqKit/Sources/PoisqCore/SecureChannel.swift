import CryptoKit
import Foundation

/// HTTP exchange used by the secure channel; replaced by a fake in tests.
protocol SecureChannelTransport: Sendable {
    func send(_ request: URLRequest) async throws -> (Data, Int)
}

/// Encrypted channel to one poisq Studio installation pinned by its QR code.
///
/// One Noise NK handshake per session, then every request is encrypted with its own key and may run in parallel.
/// The session lives only in memory: after a relaunch a new handshake runs, so a key/nonce pair is never reused.
public actor SecureChannel {
    public enum Failure: Error, Equatable {
        /// The working-key certificate does not verify against the pinned root key or rolls back the epoch.
        case untrustedCertificate
        case handshakeFailed
        /// Secure Transport is turned off in Studio.
        case disabled
        case rateLimited
        case requestTooLarge
        case malformedResponse
        case http(Int)
    }

    private struct Session {
        let id: Data
        let c2s: Data
        let s2c: Data
        let expires: Date
        let maxRequest: Int
        let maxRecord: Int
        var counter: UInt64 = 0
    }

    public nonisolated let pairing: SecureServerPairing
    /// Verified certificate of the current working key; persist it with `acceptedEpoch` per profile.
    public private(set) var certificate: SecureWorkingCertificate?
    /// Highest accepted epoch: a lower one is rejected as a rollback.
    public private(set) var acceptedEpoch: UInt32?

    private let transport: SecureChannelTransport
    private let now: @Sendable () -> Date
    private let makeEphemeral: @Sendable () -> Curve25519.KeyAgreement.PrivateKey
    private var session: Session?
    private var handshakeTask: Task<Session, Error>?

    public init(pairing: SecureServerPairing, certificate: Data? = nil, acceptedEpoch: UInt32? = nil) {
        self.init(pairing: pairing, certificate: certificate, acceptedEpoch: acceptedEpoch,
                  transport: URLSessionSecureTransport(), now: { Date() }, makeEphemeral: { .init() })
    }

    init(pairing: SecureServerPairing, certificate: Data?, acceptedEpoch: UInt32?, transport: SecureChannelTransport,
         now: @escaping @Sendable () -> Date, makeEphemeral: @escaping @Sendable () -> Curve25519.KeyAgreement.PrivateKey) {
        self.pairing = pairing
        self.transport = transport
        self.now = now
        self.makeEphemeral = makeEphemeral
        self.acceptedEpoch = acceptedEpoch
        if let certificate, let cert = SecureWorkingCertificate(data: certificate, pairing: pairing),
           cert.isAcceptable(afterAcceptedEpoch: acceptedEpoch, at: now()) {
            self.certificate = cert
        }
    }

    /// Sends one request and returns the decrypted response.
    /// An unknown session (410) is always safe to retry: the server rejects it before decrypting anything.
    public func call(_ request: Data, responseLimit: Int = 64 << 20) async throws -> Data {
        for attempt in 0..<2 {
            try await ensureSession()
            // The counter is taken synchronously from actor state: concurrent calls never share one.
            guard let (current, counter) = nextCounter() else { continue }
            guard request.count <= current.maxRequest else { throw Failure.requestTooLarge }
            let body = try SecureChannelWire.sealCall(sessionID: current.id, c2s: current.c2s, counter: counter, request: request)
            let (data, status) = try await transport.send(post("call", body))
            switch status {
            case 200:
                do {
                    return try SecureChannelWire.openRecords(data, s2c: current.s2c, header: body.prefix(SecureChannelWire.callHeaderSize),
                                                             counter: counter, maxRecord: current.maxRecord, limit: responseLimit)
                } catch {
                    throw Failure.malformedResponse
                }
            case 410:
                if session?.id == current.id { session = nil }
                if attempt == 0 { continue }
                throw Failure.http(status)
            default:
                throw Self.failure(for: status)
            }
        }
        throw Failure.handshakeFailed
    }

    /// Control exchange: confirms that this profile's channel works end to end before any credentials are sent.
    public func verify() async throws {
        let nonce = Data((0..<16).map { _ in UInt8.random(in: .min ... .max) })
        let response = try await call(Data([0x01]) + nonce)
        guard response == Data([0x00]) + nonce else { throw Failure.malformedResponse }
    }

    /// Whether a confirmed session is available right now, without network.
    public var hasValidSession: Bool {
        guard let session else { return false }
        return now() < session.expires
    }

    /// Forgets the session, for example after a network change or sign-out.
    public func reset() {
        session = nil
    }

    private func nextCounter() -> (Session, UInt64)? {
        guard var current = session, now() < current.expires else { return nil }
        current.counter += 1
        session = current
        return (current, current.counter)
    }

    /// Runs at most one handshake at a time; the handshake itself stores the session for every waiter.
    private func ensureSession() async throws {
        if let session, now() < session.expires { return }
        if let handshakeTask {
            _ = try await handshakeTask.value
            return
        }
        let task = Task { () throws -> Session in
            let fresh = try await self.performHandshake()
            self.store(fresh)
            return fresh
        }
        handshakeTask = task
        defer { handshakeTask = nil }
        _ = try await task.value
    }

    private func store(_ fresh: Session) { session = fresh }

    private func performHandshake() async throws -> Session {
        var cert = try await currentCertificate()
        for _ in 0..<2 {
            let started = now()
            var initiator = try NoiseNKInitiator(
                prologue: SecureChannelWire.prologue(installationID: pairing.installationIDBytes, epoch: cert.epoch),
                remoteStatic: cert.workingKey, ephemeral: makeEphemeral(), payload: Data())
            let (data, status) = try await transport.send(post("hello", SecureChannelWire.helloBody(epoch: cert.epoch, message1: initiator.message1)))
            switch status {
            case 200:
                guard let result = try? initiator.readMessage2(data),
                      let parameters = try? SecureChannelWire.parseHelloPayload(result.payload) else { throw Failure.handshakeFailed }
                return Session(id: parameters.id, c2s: result.initiatorToResponder, s2c: result.responderToInitiator,
                               expires: started.addingTimeInterval(TimeInterval(parameters.ttl) - 5),
                               maxRequest: Int(parameters.maxRequest), maxRecord: Int(parameters.maxRecord))
            case 409:
                // The server rotated its working key: the body is the current certificate, verified like any other.
                cert = try accept(data)
            default:
                throw Self.failure(for: status)
            }
        }
        throw Failure.handshakeFailed
    }

    private func currentCertificate() async throws -> SecureWorkingCertificate {
        if let certificate, certificate.isValid(at: now()) { return certificate }
        var request = URLRequest(url: endpoint("certificate"))
        request.httpMethod = "GET"
        let (data, status) = try await transport.send(request)
        guard status == 200 else { throw Self.failure(for: status) }
        return try accept(data)
    }

    private func accept(_ data: Data) throws -> SecureWorkingCertificate {
        guard let cert = SecureWorkingCertificate(data: data, pairing: pairing),
              cert.isAcceptable(afterAcceptedEpoch: acceptedEpoch, at: now()) else { throw Failure.untrustedCertificate }
        certificate = cert
        acceptedEpoch = max(acceptedEpoch ?? 0, cert.epoch)
        return cert
    }

    private func endpoint(_ name: String) -> URL {
        pairing.origin.appending(path: SecureChannelWire.pathPrefix + name)
    }

    private func post(_ name: String, _ body: Data) -> URLRequest {
        var request = URLRequest(url: endpoint(name))
        request.httpMethod = "POST"
        request.httpBody = body
        request.setValue("application/octet-stream", forHTTPHeaderField: "Content-Type")
        return request
    }

    private static func failure(for status: Int) -> Failure {
        switch status {
        case 404: .disabled
        case 429: .rateLimited
        case 413: .requestTooLarge
        default: .http(status)
        }
    }
}

/// URLSession without cookies, caches or redirects: a redirect is returned as a failure, never followed.
final class URLSessionSecureTransport: NSObject, SecureChannelTransport, URLSessionTaskDelegate {
    private let session: URLSession

    override init() {
        let config = URLSessionConfiguration.ephemeral
        config.httpCookieStorage = nil
        config.httpShouldSetCookies = false
        config.urlCache = nil
        config.requestCachePolicy = .reloadIgnoringLocalCacheData
        config.timeoutIntervalForRequest = 30
        session = URLSession(configuration: config)
        super.init()
    }

    func send(_ request: URLRequest) async throws -> (Data, Int) {
        let (data, response) = try await session.data(for: request, delegate: self)
        return (data, (response as? HTTPURLResponse)?.statusCode ?? 0)
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest) async -> URLRequest? {
        nil
    }
}
