// Package upload is the optional object-storage uploader for recordings
// (spec 13.3): it copies a finished recording to an S3-compatible bucket.
// It is adapted from the former artifact store's S3 adapter and keeps its
// credential handling: access and secret keys are read from files on every
// upload, so rotating them needs no restart.
package upload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"

	"github.com/minio/minio-go/v7"
	miniocreds "github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/CIPFZ/go-agent-platform/internal/credentials"
)

// Config describes the bucket. Endpoint is host[:port]; Insecure uses plain
// HTTP instead of HTTPS.
type Config struct {
	Endpoint      string
	Bucket        string
	Prefix        string
	AccessKeyFile string
	SecretKeyFile string
	Insecure      bool
}

// Enabled reports whether an uploader is configured.
func (c Config) Enabled() bool { return c.Endpoint != "" }

// ErrInvalidKey rejects object keys that could escape the prefix.
var ErrInvalidKey = errors.New("object key must be a relative path without empty, . or .. segments")

// Uploader puts objects into one bucket.
type Uploader struct{ cfg Config }

// New validates cfg. The endpoint is not contacted.
func New(cfg Config) (*Uploader, error) {
	if strings.Contains(cfg.Endpoint, "://") {
		u, err := url.Parse(cfg.Endpoint)
		if err != nil || u.Host == "" || (u.Path != "" && u.Path != "/") {
			return nil, fmt.Errorf("-upload-s3-endpoint must be host[:port], got %q", cfg.Endpoint)
		}
		if u.Scheme == "http" {
			cfg.Insecure = true
		} else if u.Scheme != "https" {
			return nil, fmt.Errorf("-upload-s3-endpoint scheme must be http or https")
		}
		cfg.Endpoint = u.Host
	}
	switch {
	case cfg.Endpoint == "":
		return nil, errors.New("-upload-s3-endpoint is required")
	case cfg.Bucket == "" || strings.ContainsAny(cfg.Bucket, "/\\\x00"):
		return nil, errors.New("-upload-s3-bucket must be a bucket name")
	case cfg.AccessKeyFile == "" || cfg.SecretKeyFile == "":
		return nil, errors.New("-upload-s3-access-key-file and -upload-s3-secret-key-file are required")
	}
	cfg.Prefix = strings.Trim(cfg.Prefix, "/")
	if cfg.Prefix != "" {
		if _, err := cleanKey(cfg.Prefix); err != nil {
			return nil, fmt.Errorf("-upload-s3-prefix: %w", err)
		}
		cfg.Prefix += "/"
	}
	return &Uploader{cfg: cfg}, nil
}

// Bucket returns the configured bucket.
func (u *Uploader) Bucket() string { return u.cfg.Bucket }

// ObjectKey returns prefix + key, validating key.
func (u *Uploader) ObjectKey(key string) (string, error) {
	k, err := cleanKey(key)
	if err != nil {
		return "", err
	}
	return u.cfg.Prefix + k, nil
}

func cleanKey(key string) (string, error) {
	if key == "" || strings.HasPrefix(key, "/") || strings.ContainsAny(key, "\\\x00") {
		return "", ErrInvalidKey
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", ErrInvalidKey
		}
	}
	return path.Clean(key), nil
}

func (u *Uploader) client() (*minio.Client, error) {
	access, err := credentials.Resolve("", u.cfg.AccessKeyFile)
	if err != nil {
		return nil, fmt.Errorf("access key: %w", err)
	}
	secret, err := credentials.Resolve("", u.cfg.SecretKeyFile)
	if err != nil {
		return nil, fmt.Errorf("secret key: %w", err)
	}
	return minio.New(u.cfg.Endpoint, &minio.Options{Creds: miniocreds.NewStaticV4(access, secret, ""), Secure: !u.cfg.Insecure})
}

// Result describes an uploaded object.
type Result struct {
	Bucket string `json:"bucket"`
	Key    string `json:"key"`
	Size   int64  `json:"size"`
	ETag   string `json:"etag"`
}

// Put streams size bytes from r to the object key (already prefixed by
// ObjectKey); large bodies use multipart upload.
func (u *Uploader) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (*Result, error) {
	c, err := u.client()
	if err != nil {
		return nil, err
	}
	info, err := c.PutObject(ctx, u.cfg.Bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return nil, err
	}
	return &Result{Bucket: u.cfg.Bucket, Key: key, Size: info.Size, ETag: info.ETag}, nil
}
