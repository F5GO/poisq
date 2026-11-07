// Package proxy реализует прозрачный HTTP Reverse Proxy для Navidrome
// с перехватом поисковых запросов и fan-out объединением результатов.
//
// Данный файл extensions.go объявляет расширение `poisq` в ответе getOpenSubsonicExtensions.
// По нему приложение poisq понимает, что за сервером стоит наш прокси, и включает надстройку.
// Остальные клиенты видят лишнюю запись в списке расширений и игнорируют её (так устроен OpenSubsonic).
package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// PoisqExtensionVersion — версия расширения poisq. Растёт, когда прокси получает новые возможности для клиента.
const PoisqExtensionVersion = 1

func isExtensionsPath(path string) bool {
	return strings.Contains(strings.ToLower(path), "/rest/getopensubsonicextensions")
}

// AdvertisePoisqExtension добавляет запись `poisq` в ответ getOpenSubsonicExtensions (JSON и XML).
// Ошибки разбора не ломают ответ: клиент получит исходный ответ сервера.
func AdvertisePoisqExtension(resp *http.Response) error {
	if resp.Request == nil || !isExtensionsPath(resp.Request.URL.Path) ||
		resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Encoding") != "" {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return err
	}

	out := body
	if strings.Contains(resp.Header.Get("Content-Type"), "json") {
		out = addExtensionJSON(body)
	} else {
		out = addExtensionXML(body)
	}

	resp.Body = io.NopCloser(bytes.NewReader(out))
	resp.ContentLength = int64(len(out))
	resp.Header.Set("Content-Length", strconv.Itoa(len(out)))
	return nil
}

func addExtensionJSON(body []byte) []byte {
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return body
	}
	var sr map[string]json.RawMessage
	if json.Unmarshal(root["subsonic-response"], &sr) != nil {
		return body
	}
	var list []json.RawMessage
	if raw, ok := sr["openSubsonicExtensions"]; ok {
		_ = json.Unmarshal(raw, &list)
	}
	for _, item := range list { // уже объявлено — ничего не делаем
		var e struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(item, &e) == nil && e.Name == "poisq" {
			return body
		}
	}
	entry, _ := json.Marshal(map[string]interface{}{"name": "poisq", "versions": []int{PoisqExtensionVersion}})
	list = append(list, entry)
	sr["openSubsonicExtensions"], _ = json.Marshal(list)
	root["subsonic-response"], _ = json.Marshal(sr)
	out, err := json.Marshal(root)
	if err != nil {
		return body
	}
	return out
}

func addExtensionXML(body []byte) []byte {
	const closing = "</subsonic-response>"
	s := string(body)
	if strings.Contains(s, `name="poisq"`) {
		return body
	}
	i := strings.LastIndex(s, closing)
	if i < 0 {
		return body
	}
	entry := `<openSubsonicExtensions name="poisq"><versions>` + strconv.Itoa(PoisqExtensionVersion) + `</versions></openSubsonicExtensions>`
	return []byte(s[:i] + entry + s[i:])
}
