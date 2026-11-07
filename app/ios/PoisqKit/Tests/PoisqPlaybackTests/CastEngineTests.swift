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
        // Как Player: запуск и весь хвост очереди за один такт — на колонку уходит одна команда
        engine.play(a)
        engine.enqueue(b)
        engine.enqueue(x)
        engine.dequeue(x)
        await waitForRequests(1)
        var calls = CastStub.seen.filter { !$0.path.hasSuffix("poisqCastStatus") }
        #expect(calls.map(\.lastPathComponent) == ["poisqCastPlay"])
        #expect(query(calls[0])["zone"] == ["kitchen"] && query(calls[0])["id"] == ["a", "b"])

        // Пока MA не сообщил новый индекс, старое состояние не считается запуском
        engine.apply(.init(state: "playing", index: 3, position: 50, volume: 20))
        #expect(started.isEmpty)
        engine.apply(.init(state: "playing", index: 0, position: 1, volume: 20))
        engine.apply(.init(state: "playing", index: 1, position: 3, volume: 20))
        #expect(started == [a, b])
        #expect(engine.state == .playing)
        engine.apply(.init(state: "playing", index: 1, position: 4, volume: 20))
        #expect(started == [a, b])                          // тот же трек — повторного события нет

        // «Назад» в той же очереди: Player запускает a и ставит b — это переход по очереди MA, без пересборки
        engine.play(a)
        engine.enqueue(b)
        await waitForRequests(2)
        // Другая очередь — замена целиком; несколько нажатий подряд склеиваются в последнее
        engine.play(b)
        engine.play(x)
        engine.enqueue(a)
        await waitForRequests(3)
        calls = CastStub.seen.filter { !$0.path.hasSuffix("poisqCastStatus") }
        #expect(calls.map(\.lastPathComponent) == ["poisqCastPlay", "poisqCastControl", "poisqCastPlay"])
        #expect(query(calls[1])["action"] == ["index"] && query(calls[1])["position"] == ["0"])
        #expect(query(calls[2])["id"] == ["x", "a"])
        engine.stop()
        await waitForRequests(5)                            // stop и clear, чтобы не попали в следующий тест
    }

    @Test func addsAndRemovesAfterStart() async throws {
        CastStub.seen = []
        let c = client()
        let engine = CastEngine(client: c, zone: "kitchen", pollInterval: .seconds(60))
        let a = try c.streamURL(for: Song(id: "a", title: "A")), b = try c.streamURL(for: Song(id: "b", title: "B")), x = try c.streamURL(for: Song(id: "x", title: "X"))
        engine.play(a)
        await waitForRequests(1)
        engine.enqueue(b)
        engine.enqueue(x)                                   // добавления склеиваются в одну команду
        await waitForRequests(2)
        engine.dequeue(x)                                   // ещё не играла — убираем с колонки по индексу 2
        await waitForRequests(3)
        let calls = CastStub.seen.filter { !$0.path.hasSuffix("poisqCastStatus") }
        try #require(calls.count == 3)
        #expect(query(calls[1])["action"] == ["add"] && query(calls[1])["id"] == ["b", "x"])
        #expect(query(calls[2])["action"] == ["remove"] && query(calls[2])["position"] == ["2"])
        engine.stop()
        await waitForRequests(5)
    }

    @Test func startsFromPositionInOneCommand() async throws {
        CastStub.seen = []
        let c = client()
        let engine = CastEngine(client: c, zone: "kitchen", pollInterval: .seconds(60))
        engine.play(try c.streamURL(for: Song(id: "a", title: "A")))
        engine.seek(to: 42.7)                               // переключение на колонку посреди песни
        await waitForRequests(1)
        let calls = CastStub.seen.filter { !$0.path.hasSuffix("poisqCastStatus") }
        #expect(calls.map(\.lastPathComponent) == ["poisqCastPlay"])
        #expect(query(calls[0])["offset"] == ["42"])
        engine.stop()
        await waitForRequests(3)
    }

    @Test func zonesParseAndCapabilityNeedsPoisq() async throws {
        CastStub.body = #"{"subsonic-response":{"status":"ok","castZones":[{"id":"k","name":"Кухня","state":"playing","volume":40}]}}"#
        defer { CastStub.body = #"{"subsonic-response":{"status":"ok"}}"# }
        let zones = try await client().castZones()
        #expect(zones == [SubsonicClient.CastZone(id: "k", name: "Кухня", state: "playing", volume: 40)])
    }
}
