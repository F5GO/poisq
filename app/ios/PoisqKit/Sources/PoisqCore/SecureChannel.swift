import CryptoKit
import Foundation

/// HTTP exchange used by the secure channel; replaced by a fake in tests.
protocol SecureChannelTransport: Sendable {
    func send(_ request: URLRequest) async throws -> (Data, Int)
}

/// Device credential issued by Studio after sign-in. The token changes on every resume, so it must be saved before use.
public struct SecureDeviceCredential: Codable, Equatable, Sendable {
    public let deviceID: Data
    public let token: Data

    public init(deviceID: Data, token: Data) {
        self.deviceID = deviceID
        self.token = token
    }
}

/// Per-profile storage of the device credential (Keychain in the app, memory in tests).
public protocol SecureCredentialStore: Sendable {
    func loadCredential() -> SecureDeviceCredential?
    func saveCredential(_ credential: SecureDeviceCredential) throws
    func deleteCredential()
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
        /// No device credential: the user has to sign in.
        case notSignedIn
        case wrongCredentials
        /// Navidrome or sign-in is temporarily unavailable; the credential is kept.
        case serverUnavailable
        case tooManyDevices
        /// The device was revoked in Studio, by a password change or as a stolen token: the credential is deleted.
        case revoked
        case invalidInput
        case forbidden
    }

    private struct Session {
        let id: Data
        let c2s: Data
        let s2c: Data
        var expires: Date
        let maxRequest: Int
        let maxRecord: Int
        var counter: UInt64 = 0
        var signedIn = false
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
    private var signInTask: Task<Void, Error>?
    private let credentials: SecureCredentialStore?

    public init(pairing: SecureServerPairing, credentials: SecureCredentialStore?, certificate: Data? = nil, acceptedEpoch: UInt32? = nil) {
        self.init(pairing: pairing, certificate: certificate, acceptedEpoch: acceptedEpoch, credentials: credentials,
                  transport: URLSessionSecureTransport(), now: { Date() }, makeEphemeral: { .init() })
    }

    init(pairing: SecureServerPairing, certificate: Data?, acceptedEpoch: UInt32?, credentials: SecureCredentialStore? = nil,
         transport: SecureChannelTransport,
         now: @escaping @Sendable () -> Date, makeEphemeral: @escaping @Sendable () -> Curve25519.KeyAgreement.PrivateKey) {
        self.credentials = credentials
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

    /// Sends an application request as the signed-in user; resumes with the stored device credential when needed.
    public func request(_ request: Data, responseLimit: Int = 64 << 20) async throws -> Data {
        try await exchange(request, signedIn: true, responseLimit: responseLimit).0
    }

    /// Subsonic response returned through the channel.
    public struct SubsonicResponse: Sendable {
        public let httpStatus: Int
        public let contentType: String
        public let body: Data
    }

    /// Subsonic method through the channel. Studio adds the user's authorization itself; credentials never leave the device here.
    public func subsonic(method: String, query: [URLQueryItem], responseLimit: Int = 64 << 20) async throws -> SubsonicResponse {
        let m = Data(method.utf8)
        guard !m.isEmpty, m.count <= 64 else { throw Failure.invalidInput }
        var c = URLComponents()
        c.queryItems = query
        let q = Data((c.percentEncodedQuery ?? "").utf8)
        let body = Data([0x10, UInt8(m.count)]) + m + SecureChannelWire.bigEndian(UInt32(q.count)) + q
        let reply = [UInt8](try await request(body, responseLimit: responseLimit))
        guard let status = reply.first else { throw Failure.malformedResponse }
        guard status == 0 else { throw status == 0x02 ? Failure.forbidden : Failure.malformedResponse }
        guard reply.count >= 4, reply.count >= 4 + Int(reply[3]) else { throw Failure.malformedResponse }
        let n = Int(reply[3])
        return SubsonicResponse(httpStatus: Int(reply[1]) << 8 | Int(reply[2]),
                                contentType: String(decoding: reply[4..<4 + n], as: UTF8.self),
                                body: Data(reply[(4 + n)...]))
    }

    /// Raw request without sign-in (control exchange and sign-in operations).
    func call(_ request: Data, responseLimit: Int = 64 << 20) async throws -> Data {
        try await exchange(request, signedIn: false, responseLimit: responseLimit).0
    }

    /// Sends one request and returns the decrypted response with the session it used.
    /// An unknown session (410) is always safe to retry: the server rejects it before decrypting anything.
    private func exchange(_ request: Data, signedIn requireSignIn: Bool, responseLimit: Int) async throws -> (Data, Data) {
        for attempt in 0..<2 {
            try await ensureSession()
            if requireSignIn { try await ensureSignedIn() }
            // The counter is taken synchronously from actor state: concurrent calls never share one.
            guard let (current, counter) = nextCounter() else { continue }
            guard request.count <= current.maxRequest else { throw Failure.requestTooLarge }
            let body = try SecureChannelWire.sealCall(sessionID: current.id, c2s: current.c2s, counter: counter, request: request)
            let (data, status) = try await transport.send(post("call", body))
            switch status {
            case 200:
                do {
                    let response = try SecureChannelWire.openRecords(data, s2c: current.s2c, header: body.prefix(SecureChannelWire.callHeaderSize),
                                                                     counter: counter, maxRecord: current.maxRecord, limit: responseLimit)
                    return (response, current.id)
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

    /// Signs in with the Navidrome account over a confirmed channel and stores the device credential.
    /// The password travels only inside this channel and only once; later sessions resume with the device token.
    public func signIn(username: String, password: String, deviceName: String) async throws {
        guard let credentials else { throw Failure.notSignedIn }
        let user = Data(username.utf8), pass = Data(password.utf8)
        let device = Data(String(deviceName.prefix(64)).utf8.prefix(255))
        guard !username.trimmingCharacters(in: .whitespaces).isEmpty, user.count <= 128, !pass.isEmpty, pass.count <= 1024 else {
            throw Failure.invalidInput
        }
        try await ensureSession()
        if session?.signedIn == true { session = nil } // another account needs a fresh session
        var body = Data([0x02, UInt8(user.count)]) + user
        body += SecureChannelWire.bigEndian(UInt16(pass.count)) + pass
        body += Data([UInt8(device.count)]) + device
        let (response, sessionID) = try await exchange(body, signedIn: false, responseLimit: 1024)
        try applyCredential(response, sessionID: sessionID, store: credentials)
    }

    /// Revokes this device in Studio (best effort) and forgets the credential and session locally.
    public func signOut() async {
        if credentials?.loadCredential() != nil {
            _ = try? await exchange(Data([0x04]), signedIn: true, responseLimit: 16)
        }
        credentials?.deleteCredential()
        session = nil
    }

    /// Whether a device credential is stored for this profile.
    public var isSignedIn: Bool { credentials?.loadCredential() != nil }

    /// Runs at most one resume at a time for the current session.
    private func ensureSignedIn() async throws {
        if session?.signedIn == true { return }
        if let signInTask {
            try await signInTask.value
            return
        }
        let task = Task { try await self.resumeWithStoredCredential() }
        signInTask = task
        defer { signInTask = nil }
        try await task.value
    }

    private func resumeWithStoredCredential() async throws {
        guard let credentials, let credential = credentials.loadCredential() else { throw Failure.notSignedIn }
        let (response, sessionID) = try await exchange(Data([0x03]) + credential.token, signedIn: false, responseLimit: 1024)
        try applyCredential(response, sessionID: sessionID, store: credentials)
    }

    /// Credential response: status · token [32] · device ID [16] · session TTL u32. The new token is saved before it is used.
    private func applyCredential(_ response: Data, sessionID: Data, store: SecureCredentialStore) throws {
        let b = [UInt8](response)
        guard let status = b.first else { throw Failure.malformedResponse }
        guard status == 0 else {
            if status == 0x08 { store.deleteCredential() }
            throw Self.signInFailure(status)
        }
        guard b.count == 53 else { throw Failure.malformedResponse }
        try store.saveCredential(SecureDeviceCredential(deviceID: Data(b[33..<49]), token: Data(b[1..<33])))
        let ttl = TimeInterval(SecureChannelWire.uint32(b[49..<53]))
        if var current = session, current.id == sessionID {
            current.signedIn = true
            current.expires = now().addingTimeInterval(max(10, ttl - 30))
            session = current
        }
    }

    private static func signInFailure(_ status: UInt8) -> Failure {
        switch status {
        case 0x02: .forbidden
        case 0x04: .wrongCredentials
        case 0x05: .rateLimited
        case 0x06: .serverUnavailable
        case 0x07: .tooManyDevices
        case 0x08: .revoked
        default: .malformedResponse
        }
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
