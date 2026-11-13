package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/F5GO/poisq/internal/artistbio"
	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/search"
)

type fakeBiographyReader struct {
	mu     sync.Mutex
	values map[string]artistbio.EffectiveBiography
	err    error
	delay  time.Duration
	calls  []string
}

func (f *fakeBiographyReader) EffectiveBiography(ctx context.Context, artistID string) (artistbio.EffectiveBiography, bool, error) {
	f.mu.Lock()
	f.calls = append(f.calls, artistID)
	value, found := f.values[artistID]
	delay, err := f.delay, f.err
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return artistbio.EffectiveBiography{}, false, ctx.Err()
		}
	}
	return value, found, err
}

func TestArtistInfoRequestGETExactPath(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/subsonic/rest/getArtistInfo2.view?id=artist_42&u=user&t=secret", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	if !captureArtistInfoRequest(req) {
		t.Fatal("valid request was not captured")
	}
	if artistInfoID(req) != "artist_42" || req.Header.Get("Accept-Encoding") != "" {
		t.Fatalf("capture mismatch id=%q encoding=%q", artistInfoID(req), req.Header.Get("Accept-Encoding"))
	}
}

func TestArtistInfoRequestPOSTRestoresBody(t *testing.T) {
	body := "u=user&t=secret&s=salt&id=artist-42&c=client"
	req := httptest.NewRequest(http.MethodPost, "/rest/getArtistInfo2", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	if !captureArtistInfoRequest(req) {
		t.Fatal("valid POST was not captured")
	}
	restored, err := io.ReadAll(req.Body)
	if err != nil || string(restored) != body || req.ContentLength != int64(len(body)) {
		t.Fatalf("body changed: %q len=%d err=%v", restored, req.ContentLength, err)
	}
}

func TestArtistInfoRequestRejectsUnsafeInputs(t *testing.T) {
	tests := []*http.Request{
		httptest.NewRequest(http.MethodGet, "/rest/notgetArtistInfo2?id=a", nil),
		httptest.NewRequest(http.MethodGet, "/rest/getArtistInfo2/extra?id=a", nil),
		httptest.NewRequest(http.MethodGet, "/rest/getArtistInfo2?id=a&id=b", nil),
		httptest.NewRequest(http.MethodGet, "/rest/getArtistInfo2?id=%27%20OR%201%3D1", nil),
		httptest.NewRequest(http.MethodPut, "/rest/getArtistInfo2?id=a", nil),
	}
	post := httptest.NewRequest(http.MethodPost, "/rest/getArtistInfo2", strings.NewReader("id=a"))
	post.Header.Set("Content-Type", "application/json")
	tests = append(tests, post)
	for index, req := range tests {
		req.Header.Set("Accept-Encoding", "gzip")
		if captureArtistInfoRequest(req) || artistInfoID(req) != "" || req.Header.Get("Accept-Encoding") != "gzip" {
			t.Fatalf("case %d was captured", index)
		}
	}
}

func TestArtistInfoRequestOversizedPOSTRemainsReadable(t *testing.T) {
	body := "id=a&x=" + strings.Repeat("z", maxArtistInfoFormBytes)
	req := httptest.NewRequest(http.MethodPost, "/rest/getArtistInfo2", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if captureArtistInfoRequest(req) {
		t.Fatal("oversized form was captured")
	}
	restored, err := io.ReadAll(req.Body)
	if err != nil || string(restored) != body {
		t.Fatalf("oversized body was damaged: len=%d err=%v", len(restored), err)
	}
}

func TestArtistInfoJSONReplacesBiographyAndPreservesFields(t *testing.T) {
	body := []byte(`{"subsonic-response":{"status":"ok","version":"1.16.1","openSubsonic":true,"artistInfo2":{"biography":"old","largeImageUrl":"https://img","similarArtist":[{"id":"2","name":"Other"}],"score":1.2300,"unknown":null}}}`)
	text := "Русское описание с кавычками \"Кино\" и <тегом>."
	out, changed := rewriteArtistInfoJSON(body, text)
	if !changed {
		t.Fatal("JSON was not changed")
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatal(err)
	}
	var response map[string]json.RawMessage
	_ = json.Unmarshal(root["subsonic-response"], &response)
	var info map[string]json.RawMessage
	_ = json.Unmarshal(response["artistInfo2"], &info)
	var got string
	_ = json.Unmarshal(info["biography"], &got)
	if got != text || string(info["largeImageUrl"]) != `"https://img"` || string(info["unknown"]) != "null" || string(response["openSubsonic"]) != "true" {
		t.Fatalf("unexpected JSON: %s", out)
	}
}

func TestArtistInfoJSONAddsMissingAndRejectsMalformed(t *testing.T) {
	body := []byte(`{"subsonic-response":{"status":"ok","artistInfo2":{"smallImageUrl":"x"}}}`)
	if out, changed := rewriteArtistInfoJSON(body, "Описание"); !changed || !bytes.Contains(out, []byte(`"biography":"Описание"`)) {
		t.Fatalf("missing biography was not added: %s", out)
	}
	invalid := [][]byte{
		[]byte(`{"subsonic-response":{"artistInfo2":{}}}`),
		[]byte(`{"subsonic-response":{"status":"failed","artistInfo2":{}}}`),
		[]byte(`{"subsonic-response":{"status":"ok","artistInfo2":{},"artistInfo2":{}}}`),
		[]byte(`{"subsonic-response":{"status":"ok","artistInfo2":{}}} trailing`),
		[]byte(`{"subsonic-response":{"status":"ok","artistInfo2":null}}`),
	}
	for _, input := range invalid {
		out, changed := rewriteArtistInfoJSON(input, "Описание")
		if changed || !bytes.Equal(out, input) {
			t.Fatalf("invalid JSON changed: %s", input)
		}
	}
}

func TestArtistInfoXMLReplacesAndEscapesBiography(t *testing.T) {
	body := []byte(`<?xml version="1.0"?><subsonic-response xmlns="http://subsonic.org/restapi" status="ok" version="1.16.1"><artistInfo2 id="a"><biography>old</biography><smallImageUrl>img</smallImageUrl><unknown x="1">keep</unknown></artistInfo2></subsonic-response>`)
	out, changed := rewriteArtistInfoXML(body, `Русский <текст> & "Кино"`)
	if !changed {
		t.Fatal("XML was not changed")
	}
	var parsed struct {
		Artist struct {
			Biography string `xml:"biography"`
			Image     string `xml:"smallImageUrl"`
			Unknown   string `xml:"unknown"`
		} `xml:"artistInfo2"`
	}
	if err := xml.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("invalid output: %v\n%s", err, out)
	}
	if parsed.Artist.Biography != `Русский <текст> & "Кино"` || parsed.Artist.Image != "img" || parsed.Artist.Unknown != "keep" {
		t.Fatalf("unexpected XML: %#v\n%s", parsed, out)
	}
}

func TestArtistInfoXMLInsertsMissingAndRejectsUnsafe(t *testing.T) {
	body := []byte(`<subsonic-response xmlns="http://subsonic.org/restapi" status="ok"><artistInfo2><smallImageUrl>x</smallImageUrl></artistInfo2></subsonic-response>`)
	if out, changed := rewriteArtistInfoXML(body, "Описание"); !changed || !bytes.Contains(out, []byte("Описание")) {
		t.Fatalf("missing XML biography was not added: %s", out)
	}
	invalid := [][]byte{
		[]byte(`<!DOCTYPE x><subsonic-response status="ok"><artistInfo2/></subsonic-response>`),
		[]byte(`<subsonic-response status="ok"><artistInfo2/><artistInfo2/></subsonic-response>`),
		[]byte(`<subsonic-response status="failed"><artistInfo2/></subsonic-response>`),
		[]byte(`<subsonic-response status="ok"><artistInfo2><biography><b>x</b></biography></artistInfo2></subsonic-response>`),
		[]byte(`<subsonic-response status="ok"><artistInfo2><wrapper><biography>x</biography></wrapper></artistInfo2></subsonic-response>`),
	}
	for _, input := range invalid {
		out, changed := rewriteArtistInfoXML(input, "Описание")
		if changed || !bytes.Equal(out, input) {
			t.Fatalf("unsafe XML changed: %s", input)
		}
	}
}

func artistInfoResponse(t *testing.T, body, contentType string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/rest/getArtistInfo2?id=artist", nil)
	if !captureArtistInfoRequest(req) {
		t.Fatal("capture failed")
	}
	response := &http.Response{
		StatusCode: http.StatusOK, Request: req,
		Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)),
	}
	response.Header.Set("Content-Type", contentType)
	response.Header.Set("ETag", `"old"`)
	response.Header.Set("Cache-Control", "private")
	response.Header.Set("Set-Cookie", "a=b")
	return response
}

func TestArtistInfoResponseRewritesHeaders(t *testing.T) {
	body := `{"subsonic-response":{"status":"ok","artistInfo2":{"biography":"old","x":1}}}`
	reader := &fakeBiographyReader{values: map[string]artistbio.EffectiveBiography{
		"artist": {ArtistID: "artist", Text: "Русское описание", Source: artistbio.ProviderWikipedia},
	}}
	resp := artistInfoResponse(t, body, "application/json; charset=utf-8")
	enrichArtistInfoResponse(resp, reader)
	out, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(out, []byte("Русское описание")) || resp.Header.Get("ETag") != "" || resp.Header.Get("Cache-Control") != "private" || resp.Header.Get("Set-Cookie") != "a=b" || resp.ContentLength != int64(len(out)) {
		t.Fatalf("unexpected response headers/body: headers=%v body=%s", resp.Header, out)
	}
}

func TestArtistInfoDeliveryUsesManualOverrideAndRestoresAutomatic(t *testing.T) {
	database, err := db.OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	store, err := artistbio.NewStore(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutIdentity(context.Background(), artistbio.ArtistIdentity{ArtistID: "artist", ArtistName: "Артист", Status: artistbio.ResolutionResolved, Confidence: 1}); err != nil {
		t.Fatal(err)
	}
	automaticText := strings.Repeat("Российский музыкальный коллектив выпустил альбом и выступал на фестивалях. ", 2)
	candidateID, err := store.PutCandidate(context.Background(), artistbio.Candidate{
		ArtistID: "artist", Provider: artistbio.ProviderWikipedia, SourceArtistID: "artist",
		SourceURL: "https://ru.wikipedia.org/wiki/Artist", OriginalText: automaticText,
		DetectedLanguage: "ru", LanguageConfidence: 0.99, RussianShare: 1, MatchConfidence: 1,
		Status: artistbio.CandidateAccepted,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SelectAutomatic(context.Background(), "artist", candidateID, time.Now()); err != nil {
		t.Fatal(err)
	}

	manualText := strings.TrimSpace(strings.Repeat("Российский исполнитель записал известную пластинку и дал серию больших концертов. ", 2))
	if err := store.SetManualOverride(context.Background(), artistbio.ManualOverride{ArtistID: "artist", Text: manualText}); err != nil {
		t.Fatal(err)
	}
	upstream := `{"subsonic-response":{"status":"ok","artistInfo2":{"biography":"upstream"}}}`
	manualResponse := artistInfoResponse(t, upstream, "application/json")
	enrichArtistInfoResponse(manualResponse, store)
	manualBody, _ := io.ReadAll(manualResponse.Body)
	if !bytes.Contains(manualBody, []byte(manualText)) {
		t.Fatalf("manual biography was not delivered: %s", manualBody)
	}

	if err := store.SetManualOverride(context.Background(), artistbio.ManualOverride{ArtistID: "artist"}); err != nil {
		t.Fatal(err)
	}
	automaticResponse := artistInfoResponse(t, upstream, "application/json")
	enrichArtistInfoResponse(automaticResponse, store)
	automaticBody, _ := io.ReadAll(automaticResponse.Body)
	if !bytes.Contains(automaticBody, []byte(strings.TrimSpace(automaticText))) {
		t.Fatalf("automatic biography was not restored: %s", automaticBody)
	}
}

func TestArtistInfoResponseRemovesEnglishOnReaderFailureAndTimeout(t *testing.T) {
	body := `{"subsonic-response":{"status":"ok","artistInfo2":{"biography":"upstream"}}}`
	for name, reader := range map[string]*fakeBiographyReader{
		"missing": {values: map[string]artistbio.EffectiveBiography{}},
		"error":   {err: errors.New("private body must not escape")},
		"timeout": {delay: artistBiographyReadTimeout + 50*time.Millisecond},
	} {
		t.Run(name, func(t *testing.T) {
			resp := artistInfoResponse(t, body, "application/json")
			enrichArtistInfoResponse(resp, reader)
			out, _ := io.ReadAll(resp.Body)
			_, _, info, ok := decodeArtistInfoJSONDocument(out)
			var biography string
			if !ok || json.Unmarshal(info["biography"], &biography) != nil || biography != "" || resp.Header.Get("ETag") != "" {
				t.Fatalf("English biography was not removed: headers=%v body=%s", resp.Header, out)
			}
		})
	}
}

func TestArtistInfoResponsePreservesVerifiedRussianUpstream(t *testing.T) {
	russian := strings.Repeat("Российская музыкальная группа выпустила несколько альбомов и выступала на крупных фестивалях. ", 2)
	body := `{"subsonic-response":{"status":"ok","artistInfo2":{"biography":` + strconv.Quote(russian) + `,"image":"keep"}}}`
	resp := artistInfoResponse(t, body, "application/json")
	enrichArtistInfoResponse(resp, &fakeBiographyReader{values: map[string]artistbio.EffectiveBiography{}})
	out, _ := io.ReadAll(resp.Body)
	if string(out) != body || resp.Header.Get("ETag") != `"old"` {
		t.Fatalf("verified Russian upstream changed: headers=%v body=%s", resp.Header, out)
	}
}

func TestArtistInfoResponseRemovesEnglishXMLWithoutLocalResult(t *testing.T) {
	body := `<subsonic-response status="ok"><artistInfo2><biography>The band released several albums and performed at major international festivals throughout its career.</biography><smallImageUrl>keep</smallImageUrl></artistInfo2></subsonic-response>`
	resp := artistInfoResponse(t, body, "application/xml")
	enrichArtistInfoResponse(resp, &fakeBiographyReader{values: map[string]artistbio.EffectiveBiography{}})
	out, _ := io.ReadAll(resp.Body)
	var parsed struct {
		Artist struct {
			Biography string `xml:"biography"`
			Image     string `xml:"smallImageUrl"`
		} `xml:"artistInfo2"`
	}
	if xml.Unmarshal(out, &parsed) != nil || parsed.Artist.Biography != "" || parsed.Artist.Image != "keep" || resp.Header.Get("ETag") != "" {
		t.Fatalf("English XML biography was not removed: headers=%v body=%s", resp.Header, out)
	}
}

func TestArtistInfoOversizePassThroughWithoutTruncation(t *testing.T) {
	body := strings.Repeat("x", maxArtistInfoResponseBytes+100)
	reader := &fakeBiographyReader{values: map[string]artistbio.EffectiveBiography{"artist": {ArtistID: "artist", Text: "Описание"}}}
	resp := artistInfoResponse(t, body, "application/json")
	enrichArtistInfoResponse(resp, reader)
	out, err := io.ReadAll(resp.Body)
	if err != nil || string(out) != body {
		t.Fatalf("oversized response truncated: len=%d err=%v", len(out), err)
	}
}

func TestArtistInfoProxyPostAndConcurrentIsolation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			body, _ := io.ReadAll(req.Body)
			values, _ := url.ParseQuery(string(body))
			if values.Get("u") != "user" || values.Get("t") != "secret" {
				http.Error(w, "lost form", http.StatusBadRequest)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"subsonic-response":{"status":"ok","artistInfo2":{"biography":"upstream","image":"keep"}}}`)
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	dict := search.NewDictionary()
	server := NewServer(target, search.NewEngine(dict), dict, nil, false)
	server.SetBiographyReader(&fakeBiographyReader{values: map[string]artistbio.EffectiveBiography{
		"a": {ArtistID: "a", Text: "Описание A"},
		"b": {ArtistID: "b", Text: "Описание Б"},
	}})

	requests := []*http.Request{
		httptest.NewRequest(http.MethodGet, "/rest/getArtistInfo2?id=a", nil),
		httptest.NewRequest(http.MethodPost, "/rest/getArtistInfo2", strings.NewReader("u=user&t=secret&id=b")),
	}
	requests[1].Header.Set("Content-Type", "application/x-www-form-urlencoded")
	wants := []string{"Описание A", "Описание Б"}
	var wg sync.WaitGroup
	for index, req := range requests {
		wg.Add(1)
		go func(index int, req *http.Request) {
			defer wg.Done()
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), wants[index]) || !strings.Contains(recorder.Body.String(), `"image":"keep"`) {
				t.Errorf("request %d mixed or failed: code=%d body=%s", index, recorder.Code, recorder.Body.String())
			}
		}(index, req)
	}
	wg.Wait()
}

func TestArtistInfoCompressedResponsePassesThrough(t *testing.T) {
	body := `{"subsonic-response":{"status":"ok","artistInfo2":{"biography":"upstream"}}}`
	reader := &fakeBiographyReader{values: map[string]artistbio.EffectiveBiography{"artist": {ArtistID: "artist", Text: "Описание"}}}
	resp := artistInfoResponse(t, body, "application/json")
	resp.Header.Set("Content-Encoding", "gzip")
	enrichArtistInfoResponse(resp, reader)
	out, _ := io.ReadAll(resp.Body)
	if string(out) != body || len(reader.calls) != 0 {
		t.Fatalf("compressed response was inspected: calls=%d body=%s", len(reader.calls), out)
	}
}

func TestArtistInfoXMLDepthLimit(t *testing.T) {
	var builder strings.Builder
	builder.WriteString(`<subsonic-response status="ok"><artistInfo2>`)
	for i := 0; i < maxArtistInfoXMLDepth+1; i++ {
		builder.WriteString(`<x>`)
	}
	for i := 0; i < maxArtistInfoXMLDepth+1; i++ {
		builder.WriteString(`</x>`)
	}
	builder.WriteString(`</artistInfo2></subsonic-response>`)
	body := []byte(builder.String())
	out, changed := rewriteArtistInfoXML(body, "Описание")
	if changed || !bytes.Equal(out, body) {
		t.Fatal("deep XML was changed")
	}
}

func TestArtistInfoResponseLengthMatchesBody(t *testing.T) {
	body := `{"subsonic-response":{"status":"ok","artistInfo2":{}}}`
	reader := &fakeBiographyReader{values: map[string]artistbio.EffectiveBiography{"artist": {ArtistID: "artist", Text: strings.Repeat("Описание ", 20)}}}
	resp := artistInfoResponse(t, body, "application/json")
	enrichArtistInfoResponse(resp, reader)
	out, _ := io.ReadAll(resp.Body)
	if resp.Header.Get("Content-Length") != strconv.Itoa(len(out)) {
		t.Fatalf("content length mismatch: %s != %d", resp.Header.Get("Content-Length"), len(out))
	}
}
