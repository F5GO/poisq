package admin

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func pcm16(samples ...int16) *bytes.Reader {
	var b bytes.Buffer
	for _, s := range samples {
		_ = binary.Write(&b, binary.LittleEndian, s)
	}
	return bytes.NewReader(b.Bytes())
}

func TestComputePeaksMinMax(t *testing.T) {
	mins, maxs, err := computePeaks(pcm16(0, 12800, -12800, 256, 100, -100), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(mins) != 2 || len(maxs) != 2 {
		t.Fatalf("ожидалось 2 пика, получено %d/%d", len(mins), len(maxs))
	}
	// первый пик: min -12800>>8 = -50, max 12800>>8 = 50
	if mins[0] != -50 || maxs[0] != 50 {
		t.Fatalf("первый пик: %d..%d", mins[0], maxs[0])
	}
	// второй пик: min -100>>8 = -1, max 256>>8 = 1
	if mins[1] != -1 || maxs[1] != 1 {
		t.Fatalf("второй пик: %d..%d", mins[1], maxs[1])
	}
}

func TestComputePeaksPartialTailAndEmpty(t *testing.T) {
	mins, _, err := computePeaks(pcm16(1000, 2000), 80)
	if err != nil || len(mins) != 1 {
		t.Fatalf("неполный хвост должен дать один пик: %v, %d", err, len(mins))
	}
	mins, _, err = computePeaks(pcm16(), 80)
	if err != nil || len(mins) != 0 {
		t.Fatalf("пустой вход: %v, %d", err, len(mins))
	}
	if _, _, err = computePeaks(pcm16(1), 0); err == nil {
		t.Fatal("perPeak=0 должен вернуть ошибку")
	}
}

func TestComputePeaksFullScale(t *testing.T) {
	mins, maxs, _ := computePeaks(pcm16(-32768, 32767), 2)
	if mins[0] != -128 || maxs[0] != 127 {
		t.Fatalf("полная шкала: %d..%d", mins[0], maxs[0])
	}
}
