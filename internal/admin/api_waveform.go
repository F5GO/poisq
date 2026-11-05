// Package admin реализует внутренний HTTP-сервер панели управления (:4535).
//
// Данный файл api_waveform.go отдаёт огибающую громкости трека (пики) для крупной волны
// в редакторе таймингов Studio. Звук декодируется ffmpeg в моно 8 кГц и сворачивается
// в 100 пар min/max в секунду: так браузеру не нужно декодировать тяжёлый FLAC (24 бит / 48 кГц),
// а ответ занимает десятки килобайт. Результат кэшируется на диске.
package admin

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/logger"
)

const (
	waveformSampleRate    = 8000
	waveformPeaksPerSec   = 100
	waveformSamplesInPeak = waveformSampleRate / waveformPeaksPerSec
	waveformTimeout       = 3 * time.Minute
)

// waveformPayload — ответ эндпоинта волны. min/max нормированы в диапазон -127..127.
type waveformPayload struct {
	Rate     int     `json:"rate"`
	Duration float64 `json:"duration"`
	Min      []int8  `json:"min"`
	Max      []int8  `json:"max"`
}

// computePeaks читает из r сырые 16-битные little-endian отсчёты и сворачивает их в пары min/max
// по perPeak отсчётов. Неполный хвост тоже даёт пик.
func computePeaks(r io.Reader, perPeak int) (mins, maxs []int8, err error) {
	if perPeak <= 0 {
		return nil, nil, errors.New("perPeak must be positive")
	}
	br := bufio.NewReaderSize(r, 64*1024)
	var buf [2]byte
	lo, hi, n := int16(32767), int16(-32768), 0
	flush := func() {
		mins = append(mins, int8(lo>>8))
		maxs = append(maxs, int8(hi>>8))
		lo, hi, n = 32767, -32768, 0
	}
	for {
		if _, rerr := io.ReadFull(br, buf[:]); rerr != nil {
			if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
				break
			}
			return nil, nil, rerr
		}
		v := int16(binary.LittleEndian.Uint16(buf[:]))
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
		n++
		if n == perPeak {
			flush()
		}
	}
	if n > 0 {
		flush()
	}
	return mins, maxs, nil
}

// buildWaveform запускает ffmpeg и считает пики аудиофайла.
func buildWaveform(ctx context.Context, audioPath string) (*waveformPayload, error) {
	ctx, cancel := context.WithTimeout(ctx, waveformTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-i", audioPath,
		"-vn", "-ac", "1", "-ar", fmt.Sprint(waveformSampleRate), "-f", "s16le", "-")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("запуск ffmpeg: %w", err)
	}
	mins, maxs, perr := computePeaks(stdout, waveformSamplesInPeak)
	werr := cmd.Wait()
	if perr != nil {
		return nil, perr
	}
	if werr != nil && len(mins) == 0 {
		return nil, fmt.Errorf("ffmpeg: %v: %s", werr, strings.TrimSpace(stderr.String()))
	}
	if len(mins) == 0 {
		return nil, errors.New("в файле нет звука")
	}
	return &waveformPayload{
		Rate:     waveformPeaksPerSec,
		Duration: float64(len(mins)) / float64(waveformPeaksPerSec),
		Min:      mins,
		Max:      maxs,
	}, nil
}

func waveformCachePath(audioPath string, fi os.FileInfo) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%d|v1", audioPath, fi.Size(), fi.ModTime().UnixNano())))
	return filepath.Join(os.TempDir(), "poisq-waveforms", hex.EncodeToString(sum[:])+".json")
}

// handleTrackWaveform отдаёт пики громкости трека для редактора таймингов.
func (s *AdminServer) handleTrackWaveform(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	trackID := strings.TrimSpace(r.URL.Query().Get("id"))
	if trackID == "" {
		writeJSONError(w, http.StatusBadRequest, "track id is required")
		return
	}
	track, err := s.tracksRepo.GetByID(trackID)
	if err != nil || track == nil {
		writeJSONError(w, http.StatusNotFound, "track not found")
		return
	}
	found := s.resolveAudioPath(track, trackID)
	if found == "" {
		writeJSONError(w, http.StatusNotFound, "audio file not found")
		return
	}
	if s.guard != nil {
		if validated, verr := s.guard.ValidateTarget(found, false); verr == nil {
			found = validated
		}
	}
	fi, err := os.Stat(found)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "audio file not found")
		return
	}

	cachePath := waveformCachePath(found, fi)
	if data, rerr := os.ReadFile(cachePath); rerr == nil && len(data) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "private, max-age=3600")
		_, _ = w.Write(data)
		return
	}

	started := time.Now()
	wf, err := buildWaveform(r.Context(), found)
	if err != nil {
		logger.Warn("[WAVEFORM] Не удалось построить волну для %s: %v", found, err)
		writeJSONError(w, http.StatusInternalServerError, "waveform failed: "+err.Error())
		return
	}
	data, err := json.Marshal(wf)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	logger.Info("[WAVEFORM] %s: %d пиков (%.0f с) за %.1f с", filepath.Base(found), len(wf.Min), wf.Duration, time.Since(started).Seconds())

	if mkErr := os.MkdirAll(filepath.Dir(cachePath), 0o755); mkErr == nil {
		tmp := cachePath + ".tmp"
		if os.WriteFile(tmp, data, 0o644) == nil {
			_ = os.Rename(tmp, cachePath)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(data)
}
