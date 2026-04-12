// Package objectstore provides an S3-compatible blob storage client.
package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ErrNotFound is returned when a requested object does not exist.
var ErrNotFound = errors.New("not found")

// Client wraps a MinIO S3 client with key prefixing.
type Client struct {
	cfg    *Config
	client *minio.Client
}

// New creates an S3 client from the given configuration.
func New(cfg *Config) (*Client, error) {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:     credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure:    cfg.Secure,
		Region:    cfg.Region,
		Transport: transport,
	})
	if err != nil {
		return nil, fmt.Errorf("new s3 client: %w", err)
	}
	return &Client{cfg: cfg, client: client}, nil
}

// Put uploads an object with the given key.
func (c *Client) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	_, err := c.client.PutObject(ctx, c.cfg.Bucket, c.fullKey(key), body, size, minio.PutObjectOptions{
		ContentType: "application/octet-stream",
	})
	if err != nil {
		return fmt.Errorf("put %s: %w", key, err)
	}
	return nil
}

// PutLargeFile uploads a local file using multipart upload.
func (c *Client) PutLargeFile(ctx context.Context, key, filePath string) error {
	_, err := c.client.FPutObject(ctx, c.cfg.Bucket, c.fullKey(key), filePath, minio.PutObjectOptions{
		ContentType: "application/octet-stream",
	})
	if err != nil {
		return fmt.Errorf("put file %s: %w", key, err)
	}
	return nil
}

// Get returns a streaming reader and size for the given key.
func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	obj, err := c.client.GetObject(ctx, c.cfg.Bucket, c.fullKey(key), minio.GetObjectOptions{})
	if err != nil {
		if isNotFound(err) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, fmt.Errorf("get %s: %w", key, err)
	}
	info, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		if isNotFound(err) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, fmt.Errorf("stat %s: %w", key, err)
	}
	return obj, info.Size, nil
}

// Head returns the size of an object without downloading it.
func (c *Client) Head(ctx context.Context, key string) (int64, error) {
	info, err := c.client.StatObject(ctx, c.cfg.Bucket, c.fullKey(key), minio.StatObjectOptions{})
	if err != nil {
		if isNotFound(err) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("head %s: %w", key, err)
	}
	return info.Size, nil
}

// Delete removes an object by key.
func (c *Client) Delete(ctx context.Context, key string) error {
	err := c.client.RemoveObject(ctx, c.cfg.Bucket, c.fullKey(key), minio.RemoveObjectOptions{})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("delete %s: %w", key, err)
	}
	return nil
}

// List returns all object keys under the given prefix.
func (c *Client) List(ctx context.Context, prefix string) ([]string, error) {
	result := make([]string, 0, 64)
	for object := range c.client.ListObjects(ctx, c.cfg.Bucket, minio.ListObjectsOptions{
		Prefix:    c.fullKey(prefix),
		Recursive: true,
	}) {
		if object.Err != nil {
			return nil, object.Err
		}
		key := object.Key
		if c.cfg.Prefix != "" {
			key, _ = strings.CutPrefix(key, c.cfg.Prefix)
		}
		if key != "" {
			result = append(result, key)
		}
	}
	return result, nil
}

// Exists reports whether an object with the given key exists.
func (c *Client) Exists(ctx context.Context, key string) (bool, error) {
	_, err := c.Head(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (c *Client) fullKey(key string) string {
	return c.cfg.fullKey(key)
}

func isNotFound(err error) bool {
	var response minio.ErrorResponse
	if errors.As(err, &response) {
		return response.Code == "NoSuchKey" || response.Code == "NoSuchBucket"
	}
	return false
}
