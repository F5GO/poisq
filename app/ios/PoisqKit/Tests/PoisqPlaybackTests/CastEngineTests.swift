import Testing
import Foundation
@testable import PoisqPlayback
import PoisqCore

/// Подменяет сеть: запоминает запросы к прокси и отвечает успехом.
final class CastStub: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var seen: [URL] = []
    nonisolated(unsafe) static var body = #"{"subsonic-response":{"status":"ok"}}"#
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        Self.seen.append(request.url!)
        let resp = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(Self.body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

@MainActor @Suite(.serialized) struct CastEngineTests {
    private func client() -> SubsonicClient {
        let cfg = URLSessionConfiguration.ephemeral
        cfg.protocolClasses = [CastStub.self]
        return SubsonicClient(baseURL: URL(string: "https://m.example.com")!, username: "u", password: "p", session: URLSession(configuration: cfg))
    }
    private func query(_ url: URL) -> [String: [String]] {
        var out: [String: [String]] = [:]
        for item in URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems ?? [] { out[item.name, default: []].append(item.value ?? "") }
        return out
    }
    private func waitForRequests(_ n: Int) async {
        for _ in 0..<200 where CastStub.seen.filter({ !$0.path.hasSuffix("poisqCastStatus") }).count < n { try? await Task.sleep(for: .milliseconds(10)) }
    }

    @Test func mirrorsQueueAndReportsTrackStart() async throws {
        CastStub.seen = []
        let c = client()
        let engine = CastEngine(client: c, zone: "kitchen", pollInterval: .seconds(60))
        var started: [URL] = []
        engine.onTrackStarted = { started.append($0) }

        let a = try c.streamURL(for: Song(id: "a", title: "A")), b = try c.streamURL(for: Song(id: "b", title: "B")), x = try c.streamURL(for: Song(id: "x", title: "X"))
        engine.play(a)
        engine.enqueue(b)
        engine.enqueue(x)
        engine.dequeue(x)                                   // ещё не играла — убираем с колонки по индексу 2
        await waitForRequests(4)

        let calls = CastStub.seen.filter { !$0.path.hasSuffix("poisqCastStatus") }
        #expect(calls.map(\.lastPathComponent) == ["poisqCastPlay", "poisqCastControl", "poisqCastControl", "poisqCastControl"])
        #expect(query(calls[0])["zone"] == ["kitchen"] && query(calls[0])["id"] == ["a"])
        #expect(query(calls[1])["action"] == ["add"] && query(calls[1])["id"] == ["b"])
        #expect(query(calls[3])["action"] == ["remove"] && query(calls[3])["position"] == ["2"])

        // Колонка перешла ко второй песне — Player узнаёт об этом по ссылке
        engine.apply(.init(state: "playing", index: 1, position: 3, volume: 20))
        #expect(started == [b])
        #expect(engine.state == .playing)
        #expect(engine.currentURL == b)
        engine.apply(.init(state: "playing", index: 1, position: 4, volume: 20))
        #expect(started == [b])                             // тот же трек — повторного события нет
        engine.stop()
    }

    @Test func zonesParseAndCapabilityNeedsPoisq() async throws {
        CastStub.body = #"{"subsonic-response":{"status":"ok","castZones":[{"id":"k","name":"Кухня","state":"playing","volume":40}]}}"#
        defer { CastStub.body = #"{"subsonic-response":{"status":"ok"}}"# }
        let zones = try await client().castZones()
        #expect(zones == [SubsonicClient.CastZone(id: "k", name: "Кухня", state: "playing", volume: 40)])
    }
}
