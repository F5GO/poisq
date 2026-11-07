// Package cast отправляет музыку из poisq на колонки и комнаты через Music Assistant (MA).
//
// Данный файл ma.go — минимальный клиент JSON-RPC API Music Assistant (POST /api, Bearer-токен).
// Команды и поля сверены с исходниками music-assistant/server (ветка dev, ноябрь 2026).
package cast

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// MA — клиент одного сервера Music Assistant.
type MA struct {
	URL   string
	Token string
	HTTP  *http.Client
}

// Player — плеер MA (колонка, группа или стереопара).
type Player struct {
	ID        string `json:"player_id"`
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Enabled   bool   `json:"enabled"`
	State     string `json:"playback_state"` // idle / paused / playing
	Volume    *int   `json:"volume_level"`
}

// Provider — экземпляр провайдера MA (нам нужны файловые: filesystem_local, filesystem_smb, filesystem_nfs).
type Provider struct {
	InstanceID string `json:"instance_id"`
	Domain     string `json:"domain"`
	Name       string `json:"name"`
}

// Queue — очередь плеера MA. Для обычного плеера queue_id совпадает с player_id.
type Queue struct {
	CurrentIndex   *int    `json:"current_index"`
	State          string  `json:"state"`
	Elapsed        float64 `json:"elapsed_time"`
	ElapsedUpdated float64 `json:"elapsed_time_last_updated"` // UTC timestamp, секунды
	Items          int     `json:"items"`
}

// Call выполняет команду MA и раскладывает результат в out (если out != nil).
func (m *MA) Call(ctx context.Context, command string, args map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"message_id": "poisq", "command": command, "args": args})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.URL, "/")+"/api", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.Token)
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("Music Assistant недоступен: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		text, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("Music Assistant отклонил токен (HTTP 401)")
		}
		return fmt.Errorf("Music Assistant %s: HTTP %d: %s", command, resp.StatusCode, strings.TrimSpace(string(text)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Players возвращает все плееры MA.
func (m *MA) Players(ctx context.Context) ([]Player, error) {
	var out []Player
	return out, m.Call(ctx, "players/all", nil, &out)
}

// Providers возвращает запущенные экземпляры провайдеров MA.
func (m *MA) Providers(ctx context.Context) ([]Provider, error) {
	var out []Provider
	return out, m.Call(ctx, "providers", nil, &out)
}
