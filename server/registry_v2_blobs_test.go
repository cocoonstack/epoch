package server

import (
	"testing"
	"time"
)

func TestResolveBlobRedirectTTL(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"", defaultBlobRedirectTTL},
		{"30m", 30 * time.Minute},
		{"2h", 2 * time.Hour},
		{"garbage", defaultBlobRedirectTTL},
		{"0", defaultBlobRedirectTTL},
		{"-5m", defaultBlobRedirectTTL},
	}
	for _, c := range cases {
		if got := resolveBlobRedirectTTL(c.raw); got != c.want {
			t.Errorf("resolveBlobRedirectTTL(%q) = %s, want %s", c.raw, got, c.want)
		}
	}
}
