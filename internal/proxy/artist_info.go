package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/F5GO/poisq/internal/artistbio"
)

const (
	maxArtistInfoFormBytes     = 64 << 10
	maxArtistInfoResponseBytes = 2 << 20
	maxArtistInfoXMLDepth      = 64
	maxArtistInfoXMLTokens     = 100000
	artistBiographyReadTimeout = 75 * time.Millisecond
)

type artistInfoContextKey struct{}

type effectiveBiographyReader interface {
	EffectiveBiography(ctx context.Context, artistID string) (artistbio.EffectiveBiography, bool, error)
}

type biographyRequester interface {
	Request(ctx context.Context, artistID string, force bool) (<-chan struct{}, artistbio.JobState, error)
}

type prefixedReadCloser struct {
	io.Reader
	io.Closer
}

func captureArtistInfoRequest(req *http.Request) bool {
	if req == nil || !isArtistInfoPath(req.URL.Path) {
		return false
	}
	var ids []string
	query, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		return false
	}
	ids = append(ids, query["id"]...)

	switch req.Method {
	case http.MethodGet:
	case http.MethodPost:
		mediaType, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
		if err != nil || !strings.EqualFold(mediaType, "application/x-www-form-urlencoded") || req.Body == nil {
			return false
		}
		originalLength := req.ContentLength
		prefix, readErr := io.ReadAll(io.LimitReader(req.Body, maxArtistInfoFormBytes+1))
		if readErr != nil || len(prefix) > maxArtistInfoFormBytes {
			req.Body = &prefixedReadCloser{Reader: io.MultiReader(bytes.NewReader(prefix), req.Body), Closer: req.Body}
			return false
		}
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(prefix))
		req.ContentLength = originalLength
		if originalLength >= 0 {
			req.Header.Set("Content-Length", strconv.FormatInt(originalLength, 10))
		}
		form, err := url.ParseQuery(string(prefix))
		if err != nil {
			return false
		}
		ids = append(ids, form["id"]...)
	default:
		return false
	}

	if len(ids) != 1 || !validArtistInfoID(ids[0]) {
		return false
	}
	*req = *req.WithContext(context.WithValue(req.Context(), artistInfoContextKey{}, ids[0]))
	req.Header.Del("Accept-Encoding")
	return true
}

func isArtistInfoPath(path string) bool {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return false
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	endpoint := strings.ToLower(parts[len(parts)-1])
	return strings.EqualFold(parts[len(parts)-2], "rest") &&
		(endpoint == "getartistinfo2" || endpoint == "getartistinfo2.view")
}

func validArtistInfoID(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > artistbio.MaxProviderIDBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func artistInfoID(req *http.Request) string {
	if req == nil {
		return ""
	}
	value, _ := req.Context().Value(artistInfoContextKey{}).(string)
	return value
}

// enrichArtistInfoResponse replaces Navidrome biography text with a verified
// local Russian biography. If no local result exists, a safely decoded
// non-Russian upstream biography is removed instead of leaking English text.
func enrichArtistInfoResponse(resp *http.Response, reader effectiveBiographyReader) {
	enrichArtistInfoResponseOnDemand(resp, reader, nil, 0)
}

func enrichArtistInfoResponseOnDemand(resp *http.Response, reader effectiveBiographyReader, requester biographyRequester, wait time.Duration) {
	if resp == nil || reader == nil || resp.Request == nil || resp.StatusCode != http.StatusOK ||
		resp.Body == nil || resp.Header.Get("Content-Encoding") != "" {
		return
	}
	artistID := artistInfoID(resp.Request)
	if !validArtistInfoID(artistID) {
		return
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	isJSON := strings.Contains(contentType, "json")
	isXML := strings.Contains(contentType, "xml")
	if !isJSON && !isXML {
		return
	}

	originalBody := resp.Body
	originalContentLength := resp.ContentLength
	originalHeaderLength, hadHeaderLength := resp.Header["Content-Length"]
	body, readErr := io.ReadAll(io.LimitReader(originalBody, maxArtistInfoResponseBytes+1))
	if readErr != nil || len(body) > maxArtistInfoResponseBytes {
		resp.Body = &prefixedReadCloser{Reader: io.MultiReader(bytes.NewReader(body), originalBody), Closer: originalBody}
		return
	}
	_ = originalBody.Close()

	replacement := ""
	shouldRewrite := false
	ctx, cancel := context.WithTimeout(resp.Request.Context(), artistBiographyReadTimeout)
	biography, found, err := reader.EffectiveBiography(ctx, artistID)
	cancel()
	if (err != nil || !found) && requester != nil && validArtistInfoDocument(body, isJSON) {
		done, _, requestErr := requester.Request(resp.Request.Context(), artistID, false)
		if requestErr == nil && done != nil {
			if wait < 5*time.Second || wait > 30*time.Second {
				wait = 25 * time.Second
			}
			timer := time.NewTimer(wait)
			select {
			case <-done:
				timer.Stop()
				readCtx, readCancel := context.WithTimeout(resp.Request.Context(), artistBiographyReadTimeout)
				biography, found, err = reader.EffectiveBiography(readCtx, artistID)
				readCancel()
			case <-resp.Request.Context().Done():
				timer.Stop()
			case <-timer.C:
			}
		}
	}
	if err == nil && found && biography.ArtistID == artistID && strings.TrimSpace(biography.Text) != "" &&
		len(biography.Text) <= artistbio.MaxBiographyBytes && utf8.ValidString(biography.Text) {
		replacement = biography.Text
		shouldRewrite = true
	} else if upstream, present := artistInfoBiography(body, isJSON); present {
		analysis := artistbio.DetectBiographyLanguage(upstream)
		if analysis.QualityOK && analysis.Language == "ru" && analysis.Confidence >= 0.90 && analysis.RussianShare >= 0.80 {
			restoreArtistInfoBody(resp, body, originalContentLength, originalHeaderLength, hadHeaderLength)
			return
		}
		shouldRewrite = true
	}
	if !shouldRewrite {
		restoreArtistInfoBody(resp, body, originalContentLength, originalHeaderLength, hadHeaderLength)
		return
	}

	var output []byte
	var changed bool
	if isJSON {
		output, changed = rewriteArtistInfoJSON(body, replacement)
	} else {
		output, changed = rewriteArtistInfoXML(body, replacement)
	}
	if !changed {
		restoreArtistInfoBody(resp, body, originalContentLength, originalHeaderLength, hadHeaderLength)
		return
	}
	resp.Body = io.NopCloser(bytes.NewReader(output))
	resp.ContentLength = int64(len(output))
	resp.TransferEncoding = nil
	resp.Header.Del("Transfer-Encoding")
	resp.Header.Set("Content-Length", strconv.Itoa(len(output)))
	for _, header := range []string{"ETag", "Content-MD5", "Digest", "Content-Digest", "Repr-Digest"} {
		resp.Header.Del(header)
	}
}

func validArtistInfoDocument(body []byte, isJSON bool) bool {
	if isJSON {
		_, _, _, ok := decodeArtistInfoJSONDocument(body)
		return ok
	}
	if len(body) == 0 || len(body) > maxArtistInfoResponseBytes || !utf8.Valid(body) {
		return false
	}
	var document struct {
		XMLName xml.Name  `xml:"subsonic-response"`
		Status  string    `xml:"status,attr"`
		Artist  *struct{} `xml:"artistInfo2"`
	}
	return xml.Unmarshal(body, &document) == nil && document.XMLName.Local == "subsonic-response" && document.Status == "ok" && document.Artist != nil
}

func restoreArtistInfoBody(resp *http.Response, body []byte, contentLength int64, headerLength []string, hadHeaderLength bool) {
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = contentLength
	if hadHeaderLength {
		resp.Header["Content-Length"] = headerLength
	} else {
		resp.Header.Del("Content-Length")
	}
}

func artistInfoBiography(body []byte, isJSON bool) (string, bool) {
	if isJSON {
		_, _, info, ok := decodeArtistInfoJSONDocument(body)
		if !ok {
			return "", false
		}
		raw, exists := info["biography"]
		var text string
		if !exists || json.Unmarshal(raw, &text) != nil || strings.TrimSpace(text) == "" {
			return "", false
		}
		return text, true
	}
	if len(body) == 0 || len(body) > maxArtistInfoResponseBytes || !utf8.Valid(body) {
		return "", false
	}
	var document struct {
		XMLName xml.Name `xml:"subsonic-response"`
		Status  string   `xml:"status,attr"`
		Artist  struct {
			Biography string `xml:"biography"`
		} `xml:"artistInfo2"`
	}
	if xml.Unmarshal(body, &document) != nil || document.XMLName.Local != "subsonic-response" || document.Status != "ok" || strings.TrimSpace(document.Artist.Biography) == "" {
		return "", false
	}
	return document.Artist.Biography, true
}

func rewriteArtistInfoJSON(body []byte, biography string) ([]byte, bool) {
	root, response, info, ok := decodeArtistInfoJSONDocument(body)
	if !ok {
		return body, false
	}
	encodedBiography, err := json.Marshal(biography)
	if err != nil {
		return body, false
	}
	info["biography"] = encodedBiography
	encodedInfo, err := json.Marshal(info)
	if err != nil {
		return body, false
	}
	response["artistInfo2"] = encodedInfo
	encodedResponse, err := json.Marshal(response)
	if err != nil {
		return body, false
	}
	root["subsonic-response"] = encodedResponse
	output, err := json.Marshal(root)
	if err != nil || len(output) > maxArtistInfoResponseBytes+artistbio.MaxBiographyBytes {
		return body, false
	}
	return output, true
}

func decodeArtistInfoJSONDocument(body []byte) (map[string]json.RawMessage, map[string]json.RawMessage, map[string]json.RawMessage, bool) {
	if len(body) == 0 || len(body) > maxArtistInfoResponseBytes || !utf8.Valid(body) || rejectDuplicateJSONKeys(body) != nil {
		return nil, nil, nil, false
	}
	var root map[string]json.RawMessage
	if decodeStrictJSON(body, &root) != nil || root == nil {
		return nil, nil, nil, false
	}
	var response map[string]json.RawMessage
	if decodeStrictJSON(root["subsonic-response"], &response) != nil || response == nil {
		return nil, nil, nil, false
	}
	var status string
	if json.Unmarshal(response["status"], &status) != nil || status != "ok" {
		return nil, nil, nil, false
	}
	var info map[string]json.RawMessage
	if decodeStrictJSON(response["artistInfo2"], &info) != nil || info == nil {
		return nil, nil, nil, false
	}
	return root, response, info, true
}

func decodeStrictJSON(body []byte, target any) error {
	if len(body) == 0 {
		return fmt.Errorf("empty json")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("trailing json data")
	}
	return nil
}

func rejectDuplicateJSONKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("invalid json key")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("duplicate json key")
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return fmt.Errorf("invalid json object")
			}
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return fmt.Errorf("invalid json array")
			}
		default:
			return fmt.Errorf("invalid json delimiter")
		}
		return nil
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("trailing json data")
	}
	return nil
}

func rewriteArtistInfoXML(body []byte, biography string) ([]byte, bool) {
	if len(body) == 0 || len(body) > maxArtistInfoResponseBytes || !utf8.Valid(body) {
		return body, false
	}
	decoder := xml.NewDecoder(bytes.NewReader(body))
	var output bytes.Buffer
	encoder := xml.NewEncoder(&output)
	depth := 0
	tokens := 0
	rootOK := false
	artistDepth := 0
	artistCount := 0
	biographyDepth := 0
	biographyCount := 0
	changed := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return body, false
		}
		tokens++
		if tokens > maxArtistInfoXMLTokens {
			return body, false
		}
		switch value := token.(type) {
		case xml.Directive:
			return body, false
		case xml.StartElement:
			depth++
			if depth > maxArtistInfoXMLDepth {
				return body, false
			}
			if depth == 1 && value.Name.Local == "subsonic-response" {
				for _, attr := range value.Attr {
					if attr.Name.Local == "status" && attr.Value == "ok" {
						rootOK = true
					}
				}
			}
			if value.Name.Local == "artistInfo2" {
				artistCount++
				if artistCount != 1 || artistDepth != 0 || biographyDepth != 0 {
					return body, false
				}
				artistDepth = depth
			}
			if artistDepth > 0 && value.Name.Local == "biography" && depth != artistDepth+1 {
				return body, false
			}
			if artistDepth > 0 && depth == artistDepth+1 && value.Name.Local == "biography" {
				biographyCount++
				if biographyCount != 1 {
					return body, false
				}
				biographyDepth = depth
				if encoder.EncodeToken(value) != nil || encoder.EncodeToken(xml.CharData([]byte(biography))) != nil {
					return body, false
				}
				changed = true
				continue
			}
			if biographyDepth > 0 {
				return body, false
			}
		case xml.EndElement:
			if biographyDepth > 0 {
				if depth != biographyDepth || value.Name.Local != "biography" {
					return body, false
				}
				if encoder.EncodeToken(value) != nil {
					return body, false
				}
				biographyDepth = 0
				depth--
				continue
			}
			if artistDepth > 0 && depth == artistDepth && value.Name.Local == "artistInfo2" {
				if biographyCount == 0 {
					start := xml.StartElement{Name: xml.Name{Space: value.Name.Space, Local: "biography"}}
					if encoder.EncodeToken(start) != nil || encoder.EncodeToken(xml.CharData([]byte(biography))) != nil || encoder.EncodeToken(start.End()) != nil {
						return body, false
					}
					changed = true
				}
				artistDepth = 0
			}
			depth--
			if depth < 0 {
				return body, false
			}
		case xml.CharData:
			if biographyDepth > 0 {
				continue
			}
		}
		if err := encoder.EncodeToken(token); err != nil {
			return body, false
		}
	}
	if depth != 0 || !rootOK || artistCount != 1 || !changed || encoder.Flush() != nil {
		return body, false
	}
	return output.Bytes(), true
}
