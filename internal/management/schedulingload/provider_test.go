package schedulingload

import (
	"testing"
	"time"
)

func TestResetDiscountFactor(t *testing.T) {
	key := "t|1"
	now := time.Now()
	cases := []struct {
		name   string
		reset  time.Time
		hasKey bool
		want   float64
	}{
		{"no reset known -> full penalty", time.Time{}, false, 1},
		{"reset already passed -> burn freely", now.Add(-time.Hour), true, 0},
		{"within full burn window -> burn freely", now.Add(3 * time.Hour), true, 0},
		{"at horizon -> full penalty", now.Add(49 * time.Hour), true, 1},
		{"beyond horizon -> full penalty", now.Add(72 * time.Hour), true, 1},
	}
	for _, tc := range cases {
		resets := map[string]time.Time{}
		if tc.hasKey {
			resets[key] = tc.reset
		}
		got := resetDiscountFactor(key, resets)
		if tc.want == 0 {
			if got != 0 {
				t.Errorf("%s: got %v want 0", tc.name, got)
			}
		} else if tc.want == 1 {
			if got != 1 {
				t.Errorf("%s: got %v want 1", tc.name, got)
			}
		}
	}
}

func TestResetDiscountFactorRampsLinearly(t *testing.T) {
	key := "t|1"
	// Midpoint between fullBurnWindow (6h) and discountHorizon (48h) is 27h,
	// which should give a factor of 0.5.
	mid := time.Now().Add(27 * time.Hour)
	got := resetDiscountFactor(key, map[string]time.Time{key: mid})
	if got < 0.45 || got > 0.55 {
		t.Errorf("midpoint factor = %v, want ~0.5", got)
	}
}

func TestApplyResetDiscountScalesRatio(t *testing.T) {
	key := "t|1"
	near := map[string]time.Time{key: time.Now().Add(time.Hour)}
	if got := applyResetDiscount(key, 0.9, near); got != 0 {
		t.Errorf("near reset: got %v want 0 (quota shed disabled)", got)
	}
	far := map[string]time.Time{key: time.Now().Add(96 * time.Hour)}
	if got := applyResetDiscount(key, 0.9, far); got != 0.9 {
		t.Errorf("far reset: got %v want 0.9 (quota shed preserved)", got)
	}
}
