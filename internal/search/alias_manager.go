// Package search реализует интеллектуальный поиск, смену раскладки,
// транслитерацию и нечеткое сопоставление поисковых запросов для Navidrome.
//
// Данный файл alias_manager.go безопасно управляет словарем музыкальных алиасов:
// загружает встроенный файл aliases.json, считывает локальные переопределения (read-only)
// и выполняет защищенную ежедневную фоновую синхронизацию с актуальной версией на GitHub.
package search

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

//go:embed embedded_aliases.json
var embeddedAliasesData []byte

// AliasManager инкапсулирует логику безопасного хранения и ежедневной синхронизации алиасов.
type AliasManager struct {
	mu            sync.RWMutex
	localFilePath string
	remoteSyncURL string
	aliases       map[string]string
	dict          *Dictionary
	httpClient    *http.Client
}

// NewAliasManager создает менеджер алиасов, загружая встроенные и локальные данные в режиме чтения.
func NewAliasManager(localPath, remoteURL string, dict *Dictionary) *AliasManager {
	mgr := &AliasManager{
		localFilePath: localPath,
		remoteSyncURL: remoteURL,
		aliases:       make(map[string]string),
		dict:          dict,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}

	// 1. Загрузка встроенных данных
	mgr.loadEmbedded()

	// 2. Безопасное чтение локального файла (если существует)
	mgr.loadLocalFile()

	// 3. Передаем объединенные алиасы в словарь
	if dict != nil {
		dict.UpdateAliases(mgr.GetAliases())
	}

	return mgr
}

func (m *AliasManager) loadEmbedded() {
	if len(embeddedAliasesData) == 0 {
		return
	}

	var parsed map[string]string
	if err := json.Unmarshal(embeddedAliasesData, &parsed); err != nil {
		log.Printf("[WARN] Ошибка чтения встроенных алиасов: %v", err)
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range parsed {
		m.aliases[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	log.Printf("[INFO] Загружено %d встроенных музыкальных алиасов", len(m.aliases))
}

func (m *AliasManager) loadLocalFile() {
	if m.localFilePath == "" {
		return
	}

	data, err := os.ReadFile(m.localFilePath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[WARN] Не удалось прочитать локальный файл %s: %v", m.localFilePath, err)
		}
		return
	}

	var localMap map[string]string
	if err := json.Unmarshal(data, &localMap); err != nil {
		log.Printf("[WARN] Некорректный формат локального JSON %s: %v", m.localFilePath, err)
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range localMap {
		m.aliases[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	log.Printf("[INFO] Загружено %d пользовательских алиасов из локального файла %s", len(localMap), m.localFilePath)
}

// GetAliases возвращает снимок всех активных алиасов.
func (m *AliasManager) GetAliases() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]string, len(m.aliases))
	for k, v := range m.aliases {
		result[k] = v
	}
	return result
}

// SyncFromRemote безопасно загружает свежий aliases.json с GitHub с ограничением размера и валидацией схемы.
func (m *AliasManager) SyncFromRemote() error {
	if m.remoteSyncURL == "" {
		return nil
	}

	parsedURL, err := url.Parse(m.remoteSyncURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return fmt.Errorf("недопустимый URL синхронизации: %s", m.remoteSyncURL)
	}

	req, err := http.NewRequest("GET", m.remoteSyncURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "poisq-alias-sync")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ошибка HTTP-запроса к %s: %w", m.remoteSyncURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("неуспешный статус ответа от GitHub: %d", resp.StatusCode)
	}

	// Защита от исчерпания памяти (DoS): ограничиваем чтение максимум 1 МБ
	const maxFileSize = 1 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFileSize))
	if err != nil {
		return fmt.Errorf("ошибка чтения ответа: %w", err)
	}

	var remoteAliases map[string]string
	if err := json.Unmarshal(body, &remoteAliases); err != nil {
		return fmt.Errorf("ошибка парсинга удаленного JSON: %w", err)
	}

	m.mu.Lock()
	for k, v := range remoteAliases {
		cleanK := strings.ToLower(strings.TrimSpace(k))
		cleanV := strings.TrimSpace(v)
		if cleanK != "" && cleanV != "" {
			m.aliases[cleanK] = cleanV
		}
	}
	currentCopy := make(map[string]string, len(m.aliases))
	for k, v := range m.aliases {
		currentCopy[k] = v
	}
	m.mu.Unlock()

	// Повторно накладываем локальные пользовательские переопределения (у них наивысший приоритет)
	m.loadLocalFile()

	if m.dict != nil {
		m.dict.UpdateAliases(m.GetAliases())
	}

	log.Printf("[INFO] Успешная синхронизация алиасов с GitHub (%s). Всего алиасов: %d", m.remoteSyncURL, len(currentCopy))
	return nil
}

// StartDailySync запускает фоновый таймер ежедневного обновления алиасов раз в 24 часа.
func (m *AliasManager) StartDailySync(ctx context.Context) {
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
			if err := m.SyncFromRemote(); err != nil {
				log.Printf("[INFO] Первичная синхронизация алиасов с GitHub: %v (используется локальная база)", err)
			}
		}

		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				log.Println("[INFO] Запуск ежедневной синхронизации aliases.json с GitHub...")
				if err := m.SyncFromRemote(); err != nil {
					log.Printf("[WARN] Ошибка ежедневной синхронизации алиасов с GitHub: %v", err)
				}
			}
		}
	}()
}
