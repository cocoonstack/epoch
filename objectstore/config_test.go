package objectstore

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadEnvFileRejectsNonEpochKeys asserts the whitelist enforced on
// keys read from the env file: a hostile entry like PATH=/tmp must NOT
// reach os.Setenv, while legitimate EPOCH_S3_* keys still pass through.
func TestLoadEnvFileRejectsNonEpochKeys(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "s3.env")
	contents := `# example env file
EPOCH_S3_ENDPOINT=https://example.com
PATH=/tmp/evil
HOME=/tmp/evil
LD_PRELOAD=/tmp/evil.so
EPOCH_S3_BUCKET=cocoonstack
SOMETHING_ELSE=ignored
`
	if err := os.WriteFile(envPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Capture and restore the variables loadEnvFile may touch.
	for _, k := range []string{
		"EPOCH_S3_ENDPOINT",
		"EPOCH_S3_BUCKET",
		"PATH",
		"HOME",
		"LD_PRELOAD",
		"SOMETHING_ELSE",
	} {
		t.Setenv(k, "sentinel-"+k)
	}

	if err := loadEnvFile(t.Context(), envPath); err != nil {
		t.Fatalf("loadEnvFile: %v", err)
	}

	if got := os.Getenv("EPOCH_S3_ENDPOINT"); got != "https://example.com" {
		t.Errorf("EPOCH_S3_ENDPOINT = %q, want EPOCH_S3_ENDPOINT to be loaded", got)
	}
	if got := os.Getenv("EPOCH_S3_BUCKET"); got != "cocoonstack" {
		t.Errorf("EPOCH_S3_BUCKET = %q, want EPOCH_S3_BUCKET to be loaded", got)
	}
	for _, hostile := range []string{"PATH", "HOME", "LD_PRELOAD", "SOMETHING_ELSE"} {
		want := "sentinel-" + hostile
		if got := os.Getenv(hostile); got != want {
			t.Errorf("%s = %q, want %q (key was overwritten via env file)", hostile, got, want)
		}
	}
}

// TestLoadEnvFileMalformedLines walks the empty-line, comment, and
// no-`=` branches.
func TestLoadEnvFileMalformedLines(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "s3.env")
	contents := "\n# comment line\nnoEqualsHere\nEPOCH_S3_REGION=us-east-1\n"
	if err := os.WriteFile(envPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("EPOCH_S3_REGION", "sentinel")

	if err := loadEnvFile(t.Context(), envPath); err != nil {
		t.Fatalf("loadEnvFile: %v", err)
	}
	if got := os.Getenv("EPOCH_S3_REGION"); got != "us-east-1" {
		t.Errorf("EPOCH_S3_REGION = %q, want us-east-1", got)
	}
}
