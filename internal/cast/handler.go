package cast

// Данный файл handler.go — эндпоинты трансляции для приложения poisq в стиле Subsonic (/rest/<метод>, ответ subsonic-response).
// Есть только у прокси poisq: другие клиенты про них не знают, приложение без poisq их не вызывает.
// Авторизацию проверяет прокси до вызова ServeHTTP.
//
//	poisqCastZones                                   → castZones: [{id, name, state, volume}]
//	poisqCastPlay    zone, id…, [index], [offset]    → заменить очередь и играть
//	poisqCastControl zone, action, [position|volume|id…]   (remove: position = индекс в очереди)
//	poisqCastStatus  zone                            → castStatus: {state, index, position, volume}

import (
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// Zone — колонка в ответе приложению.
type Zone struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	State  string `json:"state"`
	Volume int    `json:"volume"` // -1 — колонка не сообщает
}

// IsCastPath — запрос к эндпоинтам трансляции (с учётом ND_BASEURL и суффикса .view).
func IsCastPath(p string) bool { return strings.Contains(p, "/rest/poisqCast") }

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		WriteError(w, 0, "некорректный запрос")
		return
	}
	f := r.Form
	zone := f.Get("zone")
	ctx := r.Context()
	method := strings.TrimSuffix(path.Base(r.URL.Path), ".view")

	if method != "poisqCastZones" && zone == "" {
		WriteError(w, 10, "не указан параметр zone")
		return
	}

	switch method {
	case "poisqCastZones":
		players, err := s.Zones(ctx)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		zones := make([]Zone, 0, len(players))
		for _, p := range players {
			z := Zone{ID: p.ID, Name: p.Name, State: p.State, Volume: -1}
			if p.Volume != nil {
				z.Volume = *p.Volume
			}
			zones = append(zones, z)
		}
		WriteOK(w, map[string]any{"castZones": zones})

	case "poisqCastPlay":
		if len(f["id"]) == 0 {
			WriteError(w, 10, "не указан параметр id")
			return
		}
		index, _ := strconv.Atoi(f.Get("index"))
		offset, _ := strconv.Atoi(f.Get("offset"))
		if err := s.Play(ctx, zone, f["id"], max(index, 0), max(offset, 0)); err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, nil)

	case "poisqCastControl":
		position, _ := strconv.Atoi(f.Get("position"))
		volume, _ := strconv.Atoi(f.Get("volume"))
		if err := s.Control(ctx, zone, f.Get("action"), max(position, 0), volume, f["id"]); err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, nil)

	case "poisqCastStatus":
		st, err := s.Status(ctx, zone)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, map[string]any{"castStatus": st})

	default:
		WriteError(w, 70, "неизвестный метод "+method)
	}
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrUnknownZone):
		WriteError(w, 70, err.Error())
	default:
		WriteError(w, 0, err.Error())
	}
}

// WriteOK пишет успешный subsonic-response (JSON) с дополнительными полями.
func WriteOK(w http.ResponseWriter, fields map[string]any) {
	body := map[string]any{"status": "ok", "version": "1.16.1", "type": "poisq", "openSubsonic": true}
	for k, v := range fields {
		body[k] = v
	}
	writeEnvelope(w, body)
}

// WriteError пишет ошибку в формате Subsonic (HTTP 200, status=failed). Коды: 0 общая, 10 нет параметра, 40 неверный логин, 70 не найдено.
func WriteError(w http.ResponseWriter, code int, message string) {
	writeEnvelope(w, map[string]any{
		"status": "failed", "version": "1.16.1", "type": "poisq", "openSubsonic": true,
		"error": map[string]any{"code": code, "message": message},
	})
}

func writeEnvelope(w http.ResponseWriter, body map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"subsonic-response": body})
}
