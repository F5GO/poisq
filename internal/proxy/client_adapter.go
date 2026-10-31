// Package proxy реализует прозрачный HTTP Reverse Proxy для Navidrome
// с перехватом поисковых запросов и fan-out объединением результатов.
//
// Данный файл client_adapter.go реализует адаптацию результатов поиска для мобильных клиентов Subsonic
// (Arpeggi, Amperfy, play:Sub, iSub), которые на стороне устройства осуществляют локальную фильтрацию
// поисковой выдачи по исходной строке ввода пользователя (localizedCaseInsensitiveContains).
package proxy

import (
	"fmt"
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
