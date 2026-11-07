// Package proxy реализует прозрачный HTTP Reverse Proxy для Navidrome
// с перехватом поисковых запросов и fan-out объединением результатов.
//
// Данный файл client_adapter.go реализует адаптацию результатов поиска для мобильных клиентов Subsonic
// (Arpeggi, Amperfy, play:Sub, iSub), которые на стороне устройства осуществляют локальную фильтрацию
// поисковой выдачи по исходной строке ввода пользователя (localizedCaseInsensitiveContains).
package proxy

import (
	"fmt"
	"net/http"
	"strings"
)

// DecorateEntityName добавляет исходную строку запроса в скобках к названию сущности,
// если название не содержит исходный поисковый запрос пользователя.
// Это предотвращает скрытие результатов жесткими локальными фильтрами в iOS/Android клиентах.
func DecorateEntityName(currentName, rawQuery string) string {
	cleanQuery := strings.TrimSpace(rawQuery)
	if cleanQuery == "" || currentName == "" {
		return currentName
	}

	// Если название уже содержит поисковый запрос (без учета регистра), ничего не меняем
	if strings.Contains(strings.ToLower(currentName), strings.ToLower(cleanQuery)) {
		return currentName
	}

	return fmt.Sprintf("%s (%s)", currentName, cleanQuery)
}

// NeedsDecoratedTitles сообщает, нужно ли дописывать запрос в скобках к названиям для данного клиента.
// Клиенты, которые не фильтруют выдачу на устройстве (приложение poisq), получают чистые названия:
// они определяются стандартным параметром Subsonic `c` (имя клиента).
func NeedsDecoratedTitles(req *http.Request) bool {
	return !strings.EqualFold(req.URL.Query().Get("c"), "poisq")
}
