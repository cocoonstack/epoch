// Package utils provides shared helper functions used across epoch packages.
package utils

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// FirstNonEmpty returns the first non-blank value from the given strings.
func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// SHA256Hex returns the lowercase hex-encoded SHA-256 digest of data.
func SHA256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// CopyBlobExact copies exactly size bytes from body into dst. Any bytes past
// size are drained so the underlying reader can be reused, and an error is
// returned if the actual byte count does not match — both "longer" and
// "shorter" are surfaced because the manifest size is the source of truth and
// a mismatch means the registry served the wrong blob. digest is embedded in
// error messages so callers can identify which blob was bad.
func CopyBlobExact(dst io.Writer, body io.Reader, digest string, size int64) error {
	written, err := io.CopyN(dst, body, size)
	if err != nil {
		return fmt.Errorf("copy blob %s: %w", digest, err)
	}
	if extra, _ := io.Copy(io.Discard, body); extra > 0 {
		return fmt.Errorf("blob %s longer than manifest size %d (got %d extra)", digest, size, extra)
	}
	if written != size {
		return fmt.Errorf("blob %s shorter than manifest size %d (got %d)", digest, size, written)
	}
	return nil
}

// HumanSize formats a byte count as a human-readable string (e.g. "1.5G").
func HumanSize(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1fK", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%dB", b)
	}
}

// ParseRef splits a "name:tag" reference, defaulting tag to "latest".
func ParseRef(ref string) (string, string) {
	if i := strings.LastIndex(ref, ":"); i > 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, "latest"
}
