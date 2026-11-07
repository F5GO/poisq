package cast

// Данный файл service.go хранит настройки трансляции и переводит команды приложения в команды MA.
// Песню MA находит по файлу: id песни Navidrome → путь в navidrome.db → URI файлового провайдера MA
// вида `<instance_id>://track/<путь от корня медиатеки>` (так его строит create_uri в MA).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/db"
)

// TokenProvider — имя провайдера токена MA в пуле ключей Studio.
const TokenProvider = "music_assistant"

// ErrNotConfigured — в Studio не заданы адрес MA, токен, файловый источник или ни одной колонки.
var ErrNotConfigured = errors.New("трансляция не настроена в poisq Studio")

// ErrUnknownZone — колонка не разрешена в Studio (или её нет).
var ErrUnknownZone = errors.New("колонка недоступна")

// Settings — настройки трансляции, задаются в Studio → Настройки.
type Settings struct {
	URL      string   `json:"url"`      // адрес MA, например http://192.168.1.10:8095
	Provider string   `json:"provider"` // instance_id файлового провайдера MA с той же папкой музыки
	Players  []string `json:"players"`  // player_id колонок, которые видно в приложении
}

// Service — трансляция через MA. Поля задаются при старте в main.go.
type Service struct {
	DB        *sql.DB                 // poisq.db (таблица settings)
	Token     func() (string, error)  // токен MA из пула ключей
	OpenNavDB func() (*sql.DB, error) // navidrome.db (пути песен)
	HTTP      *http.Client
	Now       func() time.Time // для тестов; nil — time.Now
}

const settingsKey = "cast"

// Settings читает настройки трансляции.
func (s *Service) Settings() (Settings, error) {
	var st Settings
	raw, err := db.GetSetting(s.DB, settingsKey)
	if err != nil || raw == "" {
		return st, err
	}
	return st, json.Unmarshal([]byte(raw), &st)
}

// SaveSettings проверяет и сохраняет настройки трансляции.
func (s *Service) SaveSettings(st Settings) error {
	st.URL = strings.TrimRight(strings.TrimSpace(st.URL), "/")
	st.Provider = strings.TrimSpace(st.Provider)
	if st.URL != "" {
		u, err := url.Parse(st.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("адрес Music Assistant должен начинаться с http:// или https://")
		}
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return db.SetSetting(s.DB, settingsKey, string(raw))
}

// client собирает клиента MA из настроек; нужен только адрес и токен.
func (s *Service) client() (*MA, Settings, error) {
	st, err := s.Settings()
	if err != nil {
		return nil, st, err
	}
	if st.URL == "" {
		return nil, st, ErrNotConfigured
	}
	token, err := s.Token()
	if err != nil || token == "" {
		return nil, st, ErrNotConfigured
	}
	return &MA{URL: st.URL, Token: token, HTTP: s.HTTP}, st, nil
}

// zoneClient — клиент для управления колонкой: всё настроено и колонка разрешена в Studio.
func (s *Service) zoneClient(zone string) (*MA, Settings, error) {
	ma, st, err := s.client()
	if err != nil {
		return nil, st, err
	}
	if st.Provider == "" || len(st.Players) == 0 {
		return nil, st, ErrNotConfigured
	}
	if !slices.Contains(st.Players, zone) {
		return nil, st, ErrUnknownZone
	}
	return ma, st, nil
}

// Discover — для Studio: все плееры и файловые провайдеры MA.
func (s *Service) Discover(ctx context.Context) ([]Player, []Provider, error) {
	ma, _, err := s.client()
	if err != nil {
		return nil, nil, err
	}
	players, err := ma.Players(ctx)
	if err != nil {
		return nil, nil, err
	}
	all, err := ma.Providers(ctx)
	if err != nil {
		return nil, nil, err
	}
	var files []Provider
	for _, p := range all {
		if strings.HasPrefix(p.Domain, "filesystem") {
			files = append(files, p)
		}
	}
	return players, files, nil
}

// Zones — колонки для приложения: разрешённые в Studio, включённые и доступные.
func (s *Service) Zones(ctx context.Context) ([]Player, error) {
	ma, st, err := s.client()
	if err != nil {
		return nil, err
	}
	if st.Provider == "" || len(st.Players) == 0 {
		return nil, ErrNotConfigured
	}
	all, err := ma.Players(ctx)
	if err != nil {
		return nil, err
	}
	var out []Player
	for _, p := range all {
		if p.Available && p.Enabled && slices.Contains(st.Players, p.ID) {
			out = append(out, p)
		}
	}
	return out, nil
}

// URIs переводит id песен Navidrome в URI файлов MA.
func (s *Service) URIs(provider string, ids []string) ([]string, error) {
	nav, err := s.OpenNavDB()
	if err != nil {
		return nil, fmt.Errorf("база Navidrome недоступна: %w", err)
	}
	defer nav.Close()

	var roots []string // корни библиотек: старые Navidrome хранят в media_file абсолютные пути
	if rows, err := nav.Query("SELECT path FROM library"); err == nil {
		for rows.Next() {
			var r string
			if rows.Scan(&r) == nil {
				roots = append(roots, r)
			}
		}
		rows.Close()
	}

	uris := make([]string, 0, len(ids))
	for _, id := range ids {
		var p string
		if err := nav.QueryRow("SELECT path FROM media_file WHERE id = ?", id).Scan(&p); err != nil {
			return nil, fmt.Errorf("песня %s не найдена в Navidrome: %w", id, err)
		}
		rel, ok := relativePath(p, roots)
		if !ok {
			return nil, fmt.Errorf("путь %s вне медиатеки Navidrome", p)
		}
		uris = append(uris, provider+"://track/"+rel)
	}
	return uris, nil
}

// relativePath — путь файла от корня медиатеки.
// shortcut: считаем, что файловый источник MA смотрит в тот же корень, что и библиотека Navidrome (без подпапки); иначе нужен префикс в настройках.
func relativePath(p string, roots []string) (string, bool) {
	if !path.IsAbs(p) { // Navidrome 0.55+: путь уже от корня библиотеки
		return p, p != ""
	}
	for _, r := range roots {
		r = strings.TrimRight(r, "/")
		if r != "" && strings.HasPrefix(p, r+"/") {
			return p[len(r)+1:], true
		}
	}
	return "", false
}

// Play заменяет очередь колонки песнями и запускает воспроизведение с index и offset (секунды).
func (s *Service) Play(ctx context.Context, zone string, ids []string, index, offset int) error {
	ma, st, err := s.zoneClient(zone)
	if err != nil {
		return err
	}
	uris, err := s.URIs(st.Provider, ids)
	if err != nil {
		return err
	}
	if err := ma.Call(ctx, "player_queues/play_media", map[string]any{"queue_id": zone, "media": uris, "option": "replace"}, nil); err != nil {
		return err
	}
	if index > 0 {
		return ma.Call(ctx, "player_queues/play_index", map[string]any{"queue_id": zone, "index": index, "seek_position": offset}, nil)
	}
	if offset > 0 {
		return ma.Call(ctx, "player_queues/seek", map[string]any{"queue_id": zone, "position": offset}, nil)
	}
	return nil
}

// Control — команды пульта: play, pause, stop, clear, next, previous, seek (position), volume (volume 0..100),
// add (ids), remove (position — индекс в очереди MA), index (position — перейти к треку очереди MA).
func (s *Service) Control(ctx context.Context, zone, action string, position, volume int, ids []string) error {
	ma, st, err := s.zoneClient(zone)
	if err != nil {
		return err
	}
	q := map[string]any{"queue_id": zone}
	switch action {
	case "play", "pause", "stop", "clear", "next", "previous":
		return ma.Call(ctx, "player_queues/"+action, q, nil)
	case "seek":
		q["position"] = position
		return ma.Call(ctx, "player_queues/seek", q, nil)
	case "index": // переход к треку, уже стоящему в очереди MA: без пересборки очереди и перечитывания файлов
		q["index"] = position
		return ma.Call(ctx, "player_queues/play_index", q, nil)
	case "remove":
		q["item_id_or_index"] = position
		return ma.Call(ctx, "player_queues/delete_item", q, nil)
	case "volume":
		return ma.Call(ctx, "players/cmd/volume_set", map[string]any{"player_id": zone, "volume_level": min(max(volume, 0), 100)}, nil)
	case "add":
		uris, err := s.URIs(st.Provider, ids)
		if err != nil {
			return err
		}
		q["media"], q["option"] = uris, "add"
		return ma.Call(ctx, "player_queues/play_media", q, nil)
	}
	return fmt.Errorf("неизвестное действие %q", action)
}

// Status — состояние колонки для приложения.
type Status struct {
	State    string  `json:"state"`    // idle / paused / playing
	Index    int     `json:"index"`    // индекс текущей песни в очереди, -1 — нет
	Position float64 `json:"position"` // секунды внутри текущей песни на момент ответа
	Volume   int     `json:"volume"`   // 0..100, -1 — колонка не сообщает
}

// Status читает очередь и громкость колонки.
func (s *Service) Status(ctx context.Context, zone string) (Status, error) {
	ma, _, err := s.zoneClient(zone)
	if err != nil {
		return Status{}, err
	}
	var q Queue
	if err := ma.Call(ctx, "player_queues/get", map[string]any{"queue_id": zone}, &q); err != nil {
		return Status{}, err
	}
	var p Player
	if err := ma.Call(ctx, "players/get", map[string]any{"player_id": zone}, &p); err != nil {
		return Status{}, err
	}
	st := Status{State: q.State, Index: -1, Position: q.Elapsed, Volume: -1}
	if q.CurrentIndex != nil {
		st.Index = *q.CurrentIndex
	}
	if p.Volume != nil {
		st.Volume = *p.Volume
	}
	if q.State == "playing" && q.ElapsedUpdated > 0 { // MA обновляет позицию не каждую секунду: досчитываем
		now := time.Now
		if s.Now != nil {
			now = s.Now
		}
		st.Position += max(0, float64(now().UnixNano())/1e9-q.ElapsedUpdated)
	}
	return st, nil
}
