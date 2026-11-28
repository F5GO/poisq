import Foundation
import AudioStreaming
import AudioToolbox
import Testing
@testable import PoisqCore
@testable import PoisqPlayback

/// Поддельный канал: отдаёт куски файла и запоминает запросы.
private final class FakeFetcher: SecureMediaFetching, @unchecked Sendable {
    let file: Data
    let lock = NSLock()
    var requests: [(offset: Int, length: Int)] = []
    var failure: Error?
    var delay: Duration = .zero
    init(size: Int) { file = Data((0..<size).map { UInt8(truncatingIfNeeded: $0 &* 31 &+ $0 >> 9) }) }

    func mediaChunk(for url: URL, offset: Int, length: Int) async throws -> SecureMediaChunk {
        let failure = lock.withLock { () -> Error? in requests.append((offset, length)); return self.failure }
        if delay != .zero { try? await Task.sleep(for: delay) }
        if let failure { throw failure }
        guard offset < file.count else { return SecureMediaChunk(data: Data(), total: file.count, pastEnd: true) }
        return SecureMediaChunk(data: file.subdata(in: offset..<min(file.count, offset + length)), total: file.count, pastEnd: false)
    }
}

/// Записывает, что источник отдал плееру; по желанию «заполняет буфер» и приостанавливает источник.
private final class Recorder: AudioStreamSourceDelegate, @unchecked Sendable {
    let lock = NSLock()
    var data = Data()
    var ends = 0
    var errors: [Error] = []
    var suspendAfter: Int?
    weak var source: SecureAudioSource?

    func dataAvailable(source: CoreAudioStreamSource, data: Data) {
        let suspend = lock.withLock { () -> Bool in
            self.data.append(data)
            return suspendAfter.map { self.data.count >= $0 } ?? false
        }
        if suspend { source.suspend() }
    }
    func errorOccurred(source: CoreAudioStreamSource, error: Error) { lock.withLock { errors.append(error) } }
    func endOfFileOccurred(source: CoreAudioStreamSource) { lock.withLock { ends += 1 } }
    func metadataReceived(data: [String: String]) {}

    var snapshot: (Data, Int, Int) { lock.withLock { (data, ends, errors.count) } }
}

private func waitUntil(_ timeout: Duration = .seconds(5), _ condition: () -> Bool) async {
    let deadline = ContinuousClock.now + timeout
    while !condition(), ContinuousClock.now < deadline { try? await Task.sleep(for: .milliseconds(10)) }
}

private let trackURL = URL(string: "poisq-secure://stream?id=t1&suffix=flac")!

@Suite struct SecureAudioSourceTests {
    @Test func readsWholeTrackInOrderWithSmallFirstChunk() async throws {
        let fetcher = FakeFetcher(size: 9 << 20 + 777)
        let queue = DispatchQueue(label: "test.source")
        let source = SecureAudioSource(url: trackURL, fetcher: fetcher, queue: queue)
        let recorder = Recorder()
        source.delegate = recorder
        #expect(source.audioFileHint == kAudioFileFLACType)
        queue.sync { source.seek(at: 0) }
        await waitUntil { recorder.snapshot.1 == 1 }
        let (data, ends, errors) = recorder.snapshot
        #expect(data == fetcher.file)
        #expect(ends == 1 && errors == 0)
        #expect(queue.sync { source.length } == fetcher.file.count)
        let lengths = fetcher.lock.withLock { fetcher.requests.map(\.length) }
        #expect(lengths.first == SecureAudioSource.firstChunk)
        #expect(lengths.dropFirst().allSatisfy { $0 == SecureAudioSource.chunk })
    }

    @Test func stopsReadingAheadWhilePlayerBufferIsFull() async throws {
        let fetcher = FakeFetcher(size: 40 << 20)
        let queue = DispatchQueue(label: "test.source")
        let source = SecureAudioSource(url: trackURL, fetcher: fetcher, queue: queue)
        let recorder = Recorder()
        recorder.suspendAfter = 1 << 20
        source.delegate = recorder
        queue.sync { source.seek(at: 0) }
        try await Task.sleep(for: .milliseconds(400))
        let fetched = fetcher.lock.withLock { fetcher.requests.reduce(0) { $0 + $1.length } }
        // Буфер упреждения ограничен: пока плеер стоит, загрузка не уходит дальше readAhead + один кусок.
        #expect(fetched <= SecureAudioSource.readAhead + SecureAudioSource.chunk + SecureAudioSource.firstChunk)
        #expect(recorder.snapshot.0.count < 2 << 20)

        recorder.lock.withLock { recorder.suspendAfter = nil }
        queue.sync { source.resume() }
        await waitUntil(.seconds(10)) { recorder.snapshot.1 == 1 }
        #expect(recorder.snapshot.0 == fetcher.file)
    }

    @Test func seekRestartsFromOffsetAndDropsStaleData() async throws {
        let fetcher = FakeFetcher(size: 12 << 20)
        fetcher.delay = .milliseconds(50)
        let queue = DispatchQueue(label: "test.source")
        let source = SecureAudioSource(url: trackURL, fetcher: fetcher, queue: queue)
        let recorder = Recorder()
        source.delegate = recorder
        queue.sync { source.seek(at: 0) }
        let offset = 7_000_123
        queue.sync {
            source.seek(at: offset)
            recorder.lock.withLock { recorder.data = Data() }
        }
        await waitUntil { recorder.snapshot.1 == 1 }
        #expect(recorder.snapshot.0 == fetcher.file.subdata(in: offset..<fetcher.file.count), "data from before the seek leaked")
        #expect(queue.sync { source.position } == fetcher.file.count)
        let firstAfterSeek = fetcher.lock.withLock { fetcher.requests.first { $0.offset == offset } }
        #expect(firstAfterSeek?.length == SecureAudioSource.firstChunk, "a seek starts with a small chunk")
    }

    @Test func seekPastEndReportsEndOfFile() async throws {
        let fetcher = FakeFetcher(size: 100_000)
        let queue = DispatchQueue(label: "test.source")
        let source = SecureAudioSource(url: trackURL, fetcher: fetcher, queue: queue)
        let recorder = Recorder()
        source.delegate = recorder
        queue.sync { source.seek(at: 0) }
        await waitUntil { recorder.snapshot.1 == 1 }
        queue.sync { source.seek(at: 200_000) }
        await waitUntil { recorder.snapshot.1 == 2 }
        #expect(recorder.snapshot.1 == 2)
    }

    @Test func unrecoverableErrorIsReportedWithoutRetries() async throws {
        let fetcher = FakeFetcher(size: 1000)
        fetcher.failure = SecureChannel.Failure.forbidden
        let queue = DispatchQueue(label: "test.source")
        let source = SecureAudioSource(url: trackURL, fetcher: fetcher, queue: queue)
        let recorder = Recorder()
        source.delegate = recorder
        queue.sync { source.seek(at: 0) }
        await waitUntil { recorder.snapshot.2 == 1 }
        #expect(recorder.snapshot.2 == 1)
        #expect(fetcher.lock.withLock { fetcher.requests.count } == 1)
    }

    @Test func closeStopsFetching() async throws {
        let fetcher = FakeFetcher(size: 40 << 20)
        fetcher.delay = .milliseconds(30)
        let queue = DispatchQueue(label: "test.source")
        let source = SecureAudioSource(url: trackURL, fetcher: fetcher, queue: queue)
        source.delegate = Recorder()
        queue.sync { source.seek(at: 0) }
        try await Task.sleep(for: .milliseconds(60))
        queue.sync { source.close() }
        let count = fetcher.lock.withLock { fetcher.requests.count }
        try await Task.sleep(for: .milliseconds(200))
        #expect(fetcher.lock.withLock { fetcher.requests.count } <= count + 1)
    }

    @Test func mapsSuffixToFileType() {
        #expect(SecureAudioSource.fileType(suffix: "MP3") == kAudioFileMP3Type)
        #expect(SecureAudioSource.fileType(suffix: "m4a") == kAudioFileM4AType)
        #expect(SecureAudioSource.fileType(suffix: nil) == 0)
    }
}

@Suite struct SecureStreamURLTests {
    @Test func secureStreamURLCarriesNoCredentials() throws {
        let pairing = try #require(SecureServerPairing(code: "poisq-secure:v1:00112233445566778899aabbccddeeff:ebVWLo_mVPlAeLES6KmLp5AfhTrmlb7X4OORC60ElmQ:aHR0cHM6Ly9tdXNpYy5leGFtcGxlLmNvbTo4NDQz"))
        let client = SubsonicClient(channel: SecureChannel(pairing: pairing, credentials: nil), username: "alice")
        let song = Song(id: "song 1&x=y", title: "T", suffix: "flac")
        let url = try client.streamURL(for: song, quality: .original)
        #expect(url.scheme == "poisq-secure")
        let items = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems ?? []
        #expect(items.first { $0.name == "id" }?.value == "song 1&x=y")
        #expect(!url.absoluteString.contains("alice") && !url.absoluteString.contains("t=") && !url.absoluteString.contains("music.example.com"))
        #expect(throws: SubsonicError.self) { try client.downloadURL(for: song) }
        #expect(throws: SubsonicError.self) { try client.coverArtURL(id: "x") }
    }
}

/// Живая проверка: трек через настоящий Studio, прокси и канал. Нужны POISQ_INTEROP_URL, _CODE, _USER, _PASSWORD
/// и POISQ_INTEROP_TRACK_SHA (SHA-256 трека с id t1).
private final class LocalRewritingTransport: SecureChannelTransport, @unchecked Sendable {
    let base: URL
    let inner = URLSessionSecureTransport()
    init(base: URL) { self.base = base }
    func send(_ request: URLRequest) async throws -> (Data, Int) {
        var r = request
        r.url = base.appending(path: String(request.url!.path.dropFirst()))
        return try await inner.send(r)
    }
}

private final class MemoryStore: SecureCredentialStore, @unchecked Sendable {
    private let lock = NSLock()
    private var credential: SecureDeviceCredential?
    func loadCredential() -> SecureDeviceCredential? { lock.withLock { credential } }
    func saveCredential(_ credential: SecureDeviceCredential) throws { lock.withLock { self.credential = credential } }
    func deleteCredential() { lock.withLock { credential = nil } }
}

@Suite(.enabled(if: ProcessInfo.processInfo.environment["POISQ_INTEROP_TRACK_SHA"] != nil))
struct SecureMediaInteropTests {
    @Test func streamsTrackBitExactThroughStudio() async throws {
        let env = ProcessInfo.processInfo.environment
        let pairing = try #require(SecureServerPairing(code: env["POISQ_INTEROP_CODE"] ?? ""))
        let base = try #require(URL(string: env["POISQ_INTEROP_URL"] ?? ""))
        let channel = SecureChannel(pairing: pairing, certificate: nil, acceptedEpoch: nil, credentials: MemoryStore(),
                                    transport: LocalRewritingTransport(base: base), now: { Date() }, makeEphemeral: { .init() })
        try await channel.signIn(username: env["POISQ_INTEROP_USER"] ?? "", password: env["POISQ_INTEROP_PASSWORD"] ?? "", deviceName: "media interop")
        let client = SubsonicClient(channel: channel, username: env["POISQ_INTEROP_USER"] ?? "")

        let queue = DispatchQueue(label: "interop.source")
        let source = SecureAudioSource(url: URL(string: "poisq-secure://stream?id=t1&suffix=flac")!, fetcher: client, queue: queue)
        let recorder = Recorder()
        source.delegate = recorder
        let started = ContinuousClock.now
        queue.sync { source.seek(at: 0) }
        await waitUntil(.seconds(30)) { recorder.snapshot.1 == 1 || recorder.snapshot.2 > 0 }
        let elapsed = ContinuousClock.now - started
        let (data, ends, errors) = recorder.snapshot
        #expect(errors == 0 && ends == 1)
        let hash = SHA256Hex.of(data)
        #expect(hash == env["POISQ_INTEROP_TRACK_SHA"], "track differs after the channel")
        print("secure media interop: \(data.count) bytes in \(elapsed)")

        // Перемотка в середину: те же байты, что в файле с этого места.
        let mid = data.count / 2
        let tail = Recorder()
        source.delegate = tail
        queue.sync { source.seek(at: mid) }
        await waitUntil(.seconds(30)) { tail.snapshot.1 == 1 }
        #expect(tail.snapshot.0 == data.subdata(in: mid..<data.count))
        await channel.signOut()
    }
}

import CryptoKit
enum SHA256Hex {
    static func of(_ data: Data) -> String { SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined() }
}
