package objectstore

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds S3-compatible object store settings.
type Config struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	Prefix    string
	Secure    bool
}

// ConfigFromEnv reads S3-compatible storage settings from the environment.
// It optionally falls back to ~/.config/epoch/s3.env when values are missing.
func ConfigFromEnv(prefix string) (*Config, error) {
	envFile := firstNonEmpty(os.Getenv("EPOCH_S3_ENV_FILE"), filepath.Join(userHomeDir(), ".config", "epoch", "s3.env"))

	endpoint := os.Getenv("EPOCH_S3_ENDPOINT")
	accessKey := os.Getenv("EPOCH_S3_ACCESS_KEY")
	secretKey := os.Getenv("EPOCH_S3_SECRET_KEY")
	bucket := os.Getenv("EPOCH_S3_BUCKET")
	region := os.Getenv("EPOCH_S3_REGION")
	secureRaw := os.Getenv("EPOCH_S3_SECURE")
	prefixValue := firstNonEmpty(os.Getenv("EPOCH_S3_PREFIX"), prefix)

	if endpoint == "" || accessKey == "" || bucket == "" {
		if err := loadEnvFile(envFile); err == nil {
			endpoint = firstNonEmpty(endpoint, os.Getenv("EPOCH_S3_ENDPOINT"))
			accessKey = firstNonEmpty(accessKey, os.Getenv("EPOCH_S3_ACCESS_KEY"))
			secretKey = firstNonEmpty(secretKey, os.Getenv("EPOCH_S3_SECRET_KEY"))
			bucket = firstNonEmpty(bucket, os.Getenv("EPOCH_S3_BUCKET"))
			region = firstNonEmpty(region, os.Getenv("EPOCH_S3_REGION"))
			secureRaw = firstNonEmpty(secureRaw, os.Getenv("EPOCH_S3_SECURE"))
			prefixValue = firstNonEmpty(prefixValue, os.Getenv("EPOCH_S3_PREFIX"))
		}
	}

	if endpoint == "" || accessKey == "" || bucket == "" {
		return nil, fmt.Errorf("EPOCH_S3_ENDPOINT, EPOCH_S3_ACCESS_KEY, and EPOCH_S3_BUCKET are required")
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

// ConfigFromConfigMap reads S3-compatible storage settings from a Kubernetes ConfigMap.
// Requires kubectl to be available and configured.
func ConfigFromConfigMap(namespace, name, prefix string) (*Config, error) {
	out, err := exec.Command("kubectl", "get", "configmap", name, "-n", namespace, "-o", "json").Output()
	if err != nil {
		return nil, fmt.Errorf("kubectl get configmap %s -n %s: %w", name, namespace, err)
	}

	var cm struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(out, &cm); err != nil {
		return nil, fmt.Errorf("parse configmap: %w", err)
	}

	endpoint := cm.Data["EPOCH_S3_ENDPOINT"]
	accessKey := cm.Data["EPOCH_S3_ACCESS_KEY"]
	secretKey := cm.Data["EPOCH_S3_SECRET_KEY"]
	bucket := cm.Data["EPOCH_S3_BUCKET"]
	region := cm.Data["EPOCH_S3_REGION"]
	secureRaw := cm.Data["EPOCH_S3_SECURE"]
	prefixValue := firstNonEmpty(cm.Data["EPOCH_S3_PREFIX"], prefix)

	if endpoint == "" || accessKey == "" || bucket == "" {
		return nil, fmt.Errorf("configmap %s/%s missing EPOCH_S3_ENDPOINT, EPOCH_S3_ACCESS_KEY, or EPOCH_S3_BUCKET", namespace, name)
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
		return "", false, fmt.Errorf("empty S3 endpoint")
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", false, fmt.Errorf("parse EPOCH_S3_ENDPOINT: %w", err)
		}
		if u.Host == "" {
			return "", false, fmt.Errorf("EPOCH_S3_ENDPOINT must include a host")
		}
		secure := u.Scheme == "https"
		if secureRaw != "" {
			parsed, err := strconv.ParseBool(secureRaw)
			if err != nil {
				return "", false, fmt.Errorf("parse EPOCH_S3_SECURE: %w", err)
			}
			secure = parsed
		}
		return u.Host, secure, nil
	}

	secure := true
	if secureRaw != "" {
		parsed, err := strconv.ParseBool(secureRaw)
		if err != nil {
			return "", false, fmt.Errorf("parse EPOCH_S3_SECURE: %w", err)
		}
		secure = parsed
	}
	return raw, secure, nil
}

func loadEnvFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		os.Setenv(strings.TrimSpace(k), strings.TrimSpace(v))
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
