package server

import (
	"testing"
	"time"
)

func TestClampBlobRedirectTTL(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want time.Duration
	}{
		{30 * time.Minute, 30 * time.Minute},
		{2 * time.Hour, 2 * time.Hour},
		{0, defaultBlobRedirectTTL},
		{-5 * time.Minute, defaultBlobRedirectTTL},
		{500 * time.Millisecond, defaultBlobRedirectTTL}, // under the 1s presign floor
		{200 * time.Hour, maxBlobRedirectTTL},            // over the 7-day presign cap
	}
	for _, tc := range cases {
		if got := clampBlobRedirectTTL(tc.in); got != tc.want {
			t.Errorf("clampBlobRedirectTTL(%s) = %s, want %s", tc.in, got, tc.want)
		}
	}
}
