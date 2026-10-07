package artifacts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/credentials"
	"github.com/minio/minio-go/v7"
	minioCredentials "github.com/minio/minio-go/v7/pkg/credentials"
)

const maxObjectBytes = 2 << 20

// S3Config describes an S3-compatible object store such as MinIO. Credential
// files are read on each operation so a secret rotation does not require a
// Gateway restart.
type S3Config struct {
	Endpoint       string
	Bucket         string
	Prefix         string
	AccessKeyFile  string
	SecretKeyFile  string
	UseSSL         bool
	RequestTimeout time.Duration
}

type S3Store struct{ config S3Config }

func NewS3Store(config S3Config) (*S3Store, error) {
	if strings.TrimSpace(config.Endpoint) == "" || strings.TrimSpace(config.Bucket) == "" {
		return nil, errors.New("S3 endpoint and bucket are required")
	}
	if strings.TrimSpace(config.AccessKeyFile) == "" || strings.TrimSpace(config.SecretKeyFile) == "" {
		return nil, errors.New("S3 access and secret key files are required")
	}
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = 10 * time.Second
	}
	if strings.ContainsAny(config.Bucket, `/\\`) || strings.ContainsRune(config.Bucket, 0) {
		return nil, errors.New("invalid S3 bucket")
	}
	if config.Prefix != "" {
		config.Prefix = strings.Trim(config.Prefix, "/")
		if strings.ContainsRune(config.Prefix, 0) {
			return nil, errors.New("invalid S3 prefix")
		}
	}
	return &S3Store{config: config}, nil
}

// Health verifies local configuration and credential availability without
// making a network request. Object operations still surface endpoint errors.
func (s *S3Store) Health() error {
	_, err := s.client()
	return err
}

func (s *S3Store) objectName(key string) (string, error) {
	if key == "" || key == "." || key == ".." || strings.ContainsAny(key, `/\\`) || strings.ContainsRune(key, 0) {
		return "", ErrInvalidKey
	}
	if s.config.Prefix == "" {
		return key, nil
	}
	return path.Join(s.config.Prefix, key), nil
}

func (s *S3Store) client() (*minio.Client, error) {
	endpoint := s.config.Endpoint
	useSSL := s.config.UseSSL
	if parsed, err := url.Parse(endpoint); err == nil && parsed.Host != "" {
		endpoint = parsed.Host
		if parsed.Scheme == "https" {
			useSSL = true
		}
	} else {
		endpoint = strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")
	}
	accessKey, err := credentials.Resolve("", s.config.AccessKeyFile)
	if err != nil {
		return nil, err
	}
	secretKey, err := credentials.Resolve("", s.config.SecretKeyFile)
	if err != nil {
		return nil, err
	}
	return minio.New(endpoint, &minio.Options{Creds: minioCredentials.NewStaticV4(accessKey, secretKey, ""), Secure: useSSL})
}

func (s *S3Store) operation() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), s.config.RequestTimeout)
}

func (s *S3Store) Put(key string, data []byte) error {
	name, err := s.objectName(key)
	if err != nil {
		return err
	}
	if len(data) > maxObjectBytes {
		return errors.New("artifact object exceeds size limit")
	}
	client, err := s.client()
	if err != nil {
		return err
	}
	ctx, cancel := s.operation()
	defer cancel()
	_, err = client.PutObject(ctx, s.config.Bucket, name, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{ContentType: "application/octet-stream"})
	return err
}

func (s *S3Store) Get(key string) ([]byte, error) {
	name, err := s.objectName(key)
	if err != nil {
		return nil, err
	}
	client, err := s.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := s.operation()
	defer cancel()
	object, err := client.GetObject(ctx, s.config.Bucket, name, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer object.Close()
	data, err := io.ReadAll(io.LimitReader(object, maxObjectBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxObjectBytes {
		return nil, fmt.Errorf("artifact object exceeds size limit")
	}
	return data, nil
}

func (s *S3Store) Delete(key string) error {
	name, err := s.objectName(key)
	if err != nil {
		return err
	}
	client, err := s.client()
	if err != nil {
		return err
	}
	ctx, cancel := s.operation()
	defer cancel()
	return client.RemoveObject(ctx, s.config.Bucket, name, minio.RemoveObjectOptions{})
}
