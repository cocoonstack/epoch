package objectstore

import (
	"cmp"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	commonk8s "github.com/cocoonstack/cocoon-common/k8s"
)

// Config holds S3-compatible object store connection settings.
type Config struct {
	Endpoint  string
	AccessKey string //nolint:gosec // configuration schema field name
	SecretKey string //nolint:gosec // configuration schema field name
	Bucket    string
	Region    string
	Prefix    string
	Secure    bool
}

// ConfigFromEnv reads S3 settings from environment, falling back to ~/.config/epoch/s3.env.
func ConfigFromEnv(prefix string) (*Config, error) {
	envFile := commonk8s.EnvOrDefault("EPOCH_S3_ENV_FILE", filepath.Join(userHomeDir(), ".config", "epoch", "s3.env"))

	endpoint := os.Getenv("EPOCH_S3_ENDPOINT")
	accessKey := os.Getenv("EPOCH_S3_ACCESS_KEY")
	secretKey := os.Getenv("EPOCH_S3_SECRET_KEY")
	bucket := os.Getenv("EPOCH_S3_BUCKET")
	region := os.Getenv("EPOCH_S3_REGION")
	secureRaw := os.Getenv("EPOCH_S3_SECURE")
	prefixValue := cmp.Or(os.Getenv("EPOCH_S3_PREFIX"), prefix)

	if endpoint == "" || accessKey == "" || bucket == "" {
		if err := loadEnvFile(envFile); err == nil {
			endpoint = cmp.Or(endpoint, os.Getenv("EPOCH_S3_ENDPOINT"))
			accessKey = cmp.Or(accessKey, os.Getenv("EPOCH_S3_ACCESS_KEY"))
			secretKey = cmp.Or(secretKey, os.Getenv("EPOCH_S3_SECRET_KEY"))
			bucket = cmp.Or(bucket, os.Getenv("EPOCH_S3_BUCKET"))
			region = cmp.Or(region, os.Getenv("EPOCH_S3_REGION"))
			secureRaw = cmp.Or(secureRaw, os.Getenv("EPOCH_S3_SECURE"))
			prefixValue = cmp.Or(prefixValue, os.Getenv("EPOCH_S3_PREFIX"))
		}
	}

	if endpoint == "" || accessKey == "" || bucket == "" {
		return nil, errors.New("epoch s3 endpoint, access key, and bucket are required")
	}

	normalizedEndpoint, secure, err := normalizeEndpoint(endpoint, secureRaw)
	if err != nil {
		return nil, err
	}

	return &Config{
		Endpoint:  normalizedEndpoint,
		AccessKey: accessKey,
		SecretKey: secretKey,
		Bucket:    bucket,
		Region:    region,
		Prefix:    prefixValue,
		Secure:    secure,
	}, nil
}

func (c *Config) fullKey(key string) string {
	return c.Prefix + key
}

func normalizeEndpoint(raw, secureRaw string) (string, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false, errors.New("empty s3 endpoint")
	}

	host, defaultSecure, err := splitEndpointHost(raw)
	if err != nil {
		return "", false, err
	}

	secure, err := resolveSecure(secureRaw, defaultSecure)
	if err != nil {
		return "", false, err
	}
	return host, secure, nil
}

func splitEndpointHost(raw string) (host string, defaultSecure bool, err error) {
	if !strings.Contains(raw, "://") {
		return raw, true, nil
	}
	u, parseErr := url.Parse(raw)
	if parseErr != nil {
		return "", false, fmt.Errorf("parse s3 endpoint: %w", parseErr)
	}
	if u.Host == "" {
		return "", false, errors.New("s3 endpoint must include a host")
	}
	return u.Host, u.Scheme == "https", nil
}

func resolveSecure(secureRaw string, defaultSecure bool) (bool, error) {
	if secureRaw == "" {
		return defaultSecure, nil
	}
	parsed, err := strconv.ParseBool(secureRaw)
	if err != nil {
		return false, fmt.Errorf("parse s3 secure: %w", err)
	}
	return parsed, nil
}

func loadEnvFile(path string) error {
	data, err := os.ReadFile(path) //nolint:gosec // path comes from env var or well-known config location
	if err != nil {
		return err
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		_ = os.Setenv(strings.TrimSpace(k), strings.TrimSpace(v))
	}
	return nil
}

func userHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "/root"
	}
	return home
}
