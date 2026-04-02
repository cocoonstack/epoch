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

	"github.com/cocoonstack/epoch/internal/util"
)

// Config holds S3-compatible object store settings.
type Config struct {
	Endpoint  string
	AccessKey string //nolint:gosec // configuration schema field name
	SecretKey string //nolint:gosec // configuration schema field name
	Bucket    string
	Region    string
	Prefix    string
	Secure    bool
}

// ConfigFromEnv reads S3-compatible storage settings from the environment.
// It optionally falls back to ~/.config/epoch/s3.env when values are missing.
func ConfigFromEnv(prefix string) (*Config, error) {
	envFile := util.FirstNonEmpty(os.Getenv("EPOCH_S3_ENV_FILE"), filepath.Join(userHomeDir(), ".config", "epoch", "s3.env"))

	endpoint := os.Getenv("EPOCH_S3_ENDPOINT")
	accessKey := os.Getenv("EPOCH_S3_ACCESS_KEY")
	secretKey := os.Getenv("EPOCH_S3_SECRET_KEY")
	bucket := os.Getenv("EPOCH_S3_BUCKET")
	region := os.Getenv("EPOCH_S3_REGION")
	secureRaw := os.Getenv("EPOCH_S3_SECURE")
	prefixValue := util.FirstNonEmpty(os.Getenv("EPOCH_S3_PREFIX"), prefix)

	if endpoint == "" || accessKey == "" || bucket == "" {
		if err := loadEnvFile(envFile); err == nil {
			endpoint = util.FirstNonEmpty(endpoint, os.Getenv("EPOCH_S3_ENDPOINT"))
			accessKey = util.FirstNonEmpty(accessKey, os.Getenv("EPOCH_S3_ACCESS_KEY"))
			secretKey = util.FirstNonEmpty(secretKey, os.Getenv("EPOCH_S3_SECRET_KEY"))
			bucket = util.FirstNonEmpty(bucket, os.Getenv("EPOCH_S3_BUCKET"))
			region = util.FirstNonEmpty(region, os.Getenv("EPOCH_S3_REGION"))
			secureRaw = util.FirstNonEmpty(secureRaw, os.Getenv("EPOCH_S3_SECURE"))
			prefixValue = util.FirstNonEmpty(prefixValue, os.Getenv("EPOCH_S3_PREFIX"))
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
// Uses kubectl subprocess because this function runs in contexts where a full k8s
// client-go dependency would be too heavy (CLI tools, pullers). The ConfigMap is read
// once at startup, so the subprocess overhead is negligible.
func ConfigFromConfigMap(namespace, name, prefix string) (*Config, error) {
	out, err := exec.Command("kubectl", "get", "configmap", name, "-n", namespace, "-o", "json").Output() //nolint:gosec // trusted args from caller
	if err != nil {
		return nil, fmt.Errorf("kubectl get configmap %s -n %s: %w", name, namespace, err)
	}

	var cm struct {
		Data map[string]string `json:"data"`
	}
	if unmarshalErr := json.Unmarshal(out, &cm); unmarshalErr != nil {
		return nil, fmt.Errorf("parse configmap: %w", unmarshalErr)
	}

	endpoint := cm.Data["EPOCH_S3_ENDPOINT"]
	accessKey := cm.Data["EPOCH_S3_ACCESS_KEY"]
	secretKey := cm.Data["EPOCH_S3_SECRET_KEY"]
	bucket := cm.Data["EPOCH_S3_BUCKET"]
	region := cm.Data["EPOCH_S3_REGION"]
	secureRaw := cm.Data["EPOCH_S3_SECURE"]
	prefixValue := util.FirstNonEmpty(cm.Data["EPOCH_S3_PREFIX"], prefix)

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
	if strings.Contains(raw, "://") { //nolint:nestif // parsing logic requires conditional branching
		u, err := url.Parse(raw)
		if err != nil {
			return "", false, fmt.Errorf("parse EPOCH_S3_ENDPOINT: %w", err)
		}
		if u.Host == "" {
			return "", false, fmt.Errorf("EPOCH_S3_ENDPOINT must include a host")
		}
		secure := u.Scheme == "https"
		if secureRaw != "" {
			parsed, parseErr := strconv.ParseBool(secureRaw)
			if parseErr != nil {
				return "", false, fmt.Errorf("parse EPOCH_S3_SECURE: %w", parseErr)
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
