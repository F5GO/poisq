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
	"sync"
	"sync/atomic"
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

	mu    sync.Mutex
	zones map[string]*zoneState
	nav   *sql.DB // navidrome.db, открытая при первом удачном запросе: не открываем базу на каждую команду колонке

	token      string // токен MA на минуту: пул ключей пишет в базу при каждой выдаче, а опрос колонки идёт раз в секунду
	tokenUntil time.Time
}

// cachedToken берёт токен из пула не чаще раза в минуту; новый токен из Studio начнёт действовать в течение минуты.
func (s *Service) cachedToken() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && time.Now().Before(s.tokenUntil) {
		return s.token, nil
	}
	token, err := s.Token()
	if err != nil {
		return "", err
	}
	s.token, s.tokenUntil = token, time.Now().Add(time.Minute)
	return token, nil
}

// navDB открывает navidrome.db один раз; пока базы нет (Navidrome ещё не создал её), пробует снова при следующем запросе.
func (s *Service) navDB() (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nav == nil {
		nav, err := s.OpenNavDB()
		if err != nil {
			return nil, err
		}
		s.nav = nav
	}
	return s.nav, nil
}

// zoneState — состояние колонки между запросами.
type zoneState struct {
	play sync.Mutex // запуск и стоп по очереди: два телефона не перемешают команды одной колонки
	tail *tail      // очередь, которая ещё дописывается в MA после старта (под Service.mu)
}

func (s *Service) zoneState(zone string) *zoneState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.zones == nil {
		s.zones = map[string]*zoneState{}
	}
	if s.zones[zone] == nil {
		s.zones[zone] = &zoneState{}
	}
	return s.zones[zone]
}

// tail — хвост очереди, который дописывается в MA фоном, пока первая песня уже играет.
type tail struct {
	added atomic.Int64 // сколько элементов уже в очереди MA (с первой песней)
	stop  atomic.Bool  // новая очередь: остаток больше не нужен
	done  chan struct{}
}

// tailChunk — по сколько песен дописываем: следующая песня попадает в MA почти сразу, а MA не занят надолго одной командой.
const tailChunk = 5

// waitTail ждёт, пока в очереди MA окажется больше need элементов или хвост допишется целиком (need < 0 — ждать целиком).
// cancel — остаток хвоста больше не нужен (новая очередь): дождаться только порции, которая уже в работе.
func (s *Service) waitTail(ctx context.Context, zone string, need int64, cancel bool) error {
	z := s.zoneState(zone)
	s.mu.Lock()
	t := z.tail
	s.mu.Unlock()
	if t == nil {
		return nil
	}
	if cancel {
		t.stop.Store(true)
	}
	for {
		if need >= 0 && t.added.Load() > need {
			return nil
		}
		select {
		case <-t.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
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
	token, err := s.cachedToken()
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
	s.mu.Lock()
	s.token = "" // Studio: только что добавленный токен проверяем сразу, а не через минуту
	s.mu.Unlock()
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
	nav, err := s.navDB()
	if err != nil {
		return nil, fmt.Errorf("база Navidrome недоступна: %w", err)
	}

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
func (s *Service) Play(ctx context.Context, zone string, ids []string, offset int) error {
	ma, st, err := s.zoneClient(zone)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return errors.New("пустая очередь")
	}
	uris, err := s.URIs(st.Provider, ids)
	if err != nil {
		return err
	}
	z := s.zoneState(zone)
	z.play.Lock()
	defer z.play.Unlock()
	if err := s.waitTail(ctx, zone, -1, true); err != nil {
		return err
	}
	// replace в MA разбирает все песни очереди и только потом запускает первую, причём с нуля.
	// Поэтому: очищаем очередь, не останавливая плеер, ставим одну песню и запускаем её сразу с нужной секунды,
	// а остальное дописываем фоном.
	if err := ma.Call(ctx, "player_queues/clear", map[string]any{"queue_id": zone, "skip_stop": true}, nil); err != nil {
		return err
	}
	if err := ma.Call(ctx, "player_queues/play_media", map[string]any{"queue_id": zone, "media": uris[:1], "option": "add"}, nil); err != nil {
		return err
	}
	if err := ma.Call(ctx, "player_queues/play_index", map[string]any{"queue_id": zone, "index": 0, "seek_position": max(offset, 0)}, nil); err != nil {
		return err
	}
	if len(uris) > 1 {
		t := &tail{done: make(chan struct{})}
		t.added.Store(1)
		s.mu.Lock()
		z.tail = t
		s.mu.Unlock()
		go s.appendTail(context.WithoutCancel(ctx), ma, zone, t, uris[1:])
	}
	return nil
}

func (s *Service) appendTail(ctx context.Context, ma *MA, zone string, t *tail, uris []string) {
	defer func() {
		close(t.done)
		z := s.zoneState(zone)
		s.mu.Lock()
		if z.tail == t {
			z.tail = nil
		}
		s.mu.Unlock()
	}()
	for len(uris) > 0 && !t.stop.Load() {
		n := min(tailChunk, len(uris))
		if err := ma.Call(ctx, "player_queues/play_media", map[string]any{"queue_id": zone, "media": uris[:n], "option": "add"}, nil); err != nil {
			return // shortcut: остаток очереди теряется до следующего запуска; приложение не узнаёт об этом, пока не дойдёт до конца
		}
		t.added.Add(int64(n))
		uris = uris[n:]
	}
}

// Control — команды пульта: play, pause, stop, clear, next, previous, seek (position), volume (volume 0..100),
// add (ids), remove (position — индекс в очереди MA), index (position — перейти к треку очереди MA).
func (s *Service) Control(ctx context.Context, zone, action string, position, volume int, ids []string) error {
	ma, st, err := s.zoneClient(zone)
	if err != nil {
		return err
	}
	q := map[string]any{"queue_id": zone}
	// Правка очереди и переход к песне — только когда нужные элементы уже в MA; пауза, громкость и перемотка не ждут.
	switch action {
	case "stop", "clear": // очередь больше не нужна: иначе хвост допишется в уже очищенную очередь
		z := s.zoneState(zone)
		z.play.Lock() // и не проскочить мимо запуска, который в этот момент идёт с другого телефона
		defer z.play.Unlock()
		err = s.waitTail(ctx, zone, -1, true)
	case "add", "remove":
		err = s.waitTail(ctx, zone, -1, false)
	case "index":
		err = s.waitTail(ctx, zone, int64(position), false)
	}
	if err != nil {
		return err
	}
	switch action {
	case "play", "pause", "stop", "clear", "next", "previous":
		return ma.Call(ctx, "player_queues/"+action, q, nil)
	case "seek":
		// Перемотка — тот же быстрый путь, что и переход к песне: play_index на текущую с нужной секунды.
		// player_queues/seek на Яндекс Станции запускается заметно дольше.
		var cur Queue
		if err := ma.Call(ctx, "player_queues/get", q, &cur); err != nil {
			return err
		}
		if cur.CurrentIndex == nil {
			return errors.New("на колонке нет текущей песни")
		}
		q["index"], q["seek_position"] = *cur.CurrentIndex, position
		return ma.Call(ctx, "player_queues/play_index", q, nil)
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
