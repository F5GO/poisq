// Package metadata отвечает за аудит медиатеки, исправление битой кодировки (CP1251 -> UTF-8 NFC),
// акустическую идентификацию через AcoustID (fpcalc), обогащение через MusicBrainz API и поиск обложек.
//
// Данный файл acoustid_fpcalc.go реализует вызов системной утилиты fpcalc (Chromaprint)
// и запрос к AcoustID API с ротацией ключей для распознавания треков со стертыми/битыми тегами
// и проверки реальной длительности (защита от путаницы со студийными и Live/Remix версиями).
package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/httpclient"
	"github.com/F5GO/poisq/internal/keypool"
	"github.com/F5GO/poisq/internal/security"
)

const acoustidLookupURL = "https://api.acoustid.org/v2/lookup"

type fpcalcOutput struct {
	Duration    float64 `json:"duration"`
	Fingerprint string  `json:"fingerprint"`
}

type acoustidResponse struct {
	Status  string `json:"status"`
	Results []struct {
		ID         string  `json:"id"`
		Score      float64 `json:"score"`
		Recordings []struct {
			ID       string  `json:"id"`
			Title    string  `json:"title"`
			Duration float64 `json:"duration"`
			Artists  []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"artists"`
			ReleaseGroups []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
				Type  string `json:"type"`
			} `json:"releasegroups"`
		} `json:"recordings"`
	} `json:"results"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// AcoustIDMatch содержит распознанные по звуку метаданные трека.
type AcoustIDMatch struct {
	RecordingMBID string  `json:"recordingMbid"`
	ArtistMBID    string  `json:"artistMbid"`
	Artist        string  `json:"artist"`
	Title         string  `json:"title"`
	Album         string  `json:"album"`
	DurationSec   int     `json:"durationSec"`
	Confidence    int     `json:"confidence"` // 0..100
	ActualDurSec  int     `json:"actualDurSec"`
	Score         float64 `json:"score"`
}

// AcoustIDClient выполняет снятие акустического отпечатка через fpcalc и поиск в AcoustID.
type AcoustIDClient struct {
	guard   *security.PathGuard
	keyPool *keypool.Manager
	limiter *httpclient.ProviderLimiter
	client  *http.Client
}

// NewAcoustIDClient создает новый экземпляр клиента AcoustID (лимит 3 запроса/сек по спецификации AcoustID).
func NewAcoustIDClient(guard *security.PathGuard, keyPool *keypool.Manager) *AcoustIDClient {
	return &AcoustIDClient{
		guard:   guard,
		keyPool: keyPool,
		limiter: httpclient.NewProviderLimiter(2, 350*time.Millisecond),
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// IdentifyFile снимает Chromaprint отпечаток файла через fpcalc и опрашивает базу AcoustID.
func (a *AcoustIDClient) IdentifyFile(ctx context.Context, audioFilePath string) (*AcoustIDMatch, error) {
	fp, err := a.runFpcalc(ctx, audioFilePath)
	if err != nil {
		return nil, err
	}

	release, err := a.limiter.Wait(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	leased, err := a.keyPool.Acquire("acoustid")
	if err != nil {
		return nil, fmt.Errorf("нет активного ключа AcoustID в пуле: %w", err)
	}

	form := url.Values{}
	form.Set("client", leased.PlainKey)
	form.Set("duration", fmt.Sprintf("%d", int(math.Round(fp.Duration))))
	form.Set("fingerprint", fp.Fingerprint)
	form.Set("meta", "recordings releasegroups compress")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, acoustidLookupURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "poisq/0.2.0 ( https://teedi.ru )")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		a.keyPool.ReportRateLimit429(leased.ID, "AcoustID 429 rate limit")
		return nil, fmt.Errorf("AcoustID HTTP 429")
	}

	var parsed acoustidResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}

	if parsed.Error != nil {
		if parsed.Error.Code == 4 { // invalid API key
			a.keyPool.ReportInvalid(leased.ID, 401, parsed.Error.Message)
		}
		return nil, fmt.Errorf("AcoustID error %d: %s", parsed.Error.Code, parsed.Error.Message)
	}

	a.keyPool.ReportSuccess(leased.ID)

	actualSec := int(math.Round(fp.Duration))
	for _, res := range parsed.Results {
		if res.Score < 0.65 {
			continue
		}
		for _, rec := range res.Recordings {
			if rec.Title == "" || len(rec.Artists) == 0 {
				continue
			}
			recDur := int(math.Round(rec.Duration))
			// Защита от путаницы с Live-версиями и ремиксами: сверяем длительность (допуск 6 сек)
			if recDur > 0 && actualSec > 0 && math.Abs(float64(recDur-actualSec)) > 6 {
				continue
			}

			albumTitle := ""
			for _, rg := range rec.ReleaseGroups {
				if rg.Title != "" {
					albumTitle = rg.Title
					if strings.EqualFold(rg.Type, "Album") {
						break
					}
				}
			}

			return &AcoustIDMatch{
				RecordingMBID: rec.ID,
				ArtistMBID:    rec.Artists[0].ID,
				Artist:        NormalizeNFC(rec.Artists[0].Name),
				Title:         NormalizeNFC(rec.Title),
				Album:         NormalizeNFC(albumTitle),
				DurationSec:   recDur,
				ActualDurSec:  actualSec,
				Score:         res.Score,
				Confidence:    int(math.Round(res.Score * 100)),
			}, nil
		}
	}

	return nil, fmt.Errorf("совпадений в AcoustID не найдено")
}

func (a *AcoustIDClient) runFpcalc(ctx context.Context, audioFilePath string) (*fpcalcOutput, error) {
	safePath, err := a.guard.ValidateTarget(audioFilePath, false)
	if err != nil {
		return nil, err
	}

	fCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(fCtx, "fpcalc", "-json", safePath)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ошибка вызова fpcalc (%v): %s", err, errBuf.String())
	}

	var parsed fpcalcOutput
	if err := json.Unmarshal(outBuf.Bytes(), &parsed); err != nil {
		return nil, fmt.Errorf("некорректный JSON от fpcalc: %w", err)
	}
	if parsed.Fingerprint == "" || parsed.Duration <= 0 {
		return nil, fmt.Errorf("fpcalc вернул пустой отпечаток")
	}
	return &parsed, nil
}
