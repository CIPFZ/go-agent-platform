package upload

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	miniocreds "github.com/minio/minio-go/v7/pkg/credentials"
)

func keyFiles(t *testing.T, access, secret string) (string, string) {
	dir := t.TempDir()
	a, s := filepath.Join(dir, "access"), filepath.Join(dir, "secret")
	os.WriteFile(a, []byte(access+"\n"), 0o600)
	os.WriteFile(s, []byte(secret+"\n"), 0o600)
	return a, s
}

func TestConfigValidation(t *testing.T) {
	a, s := keyFiles(t, "k", "v")
	ok := Config{Endpoint: "minio:9000", Bucket: "b", AccessKeyFile: a, SecretKeyFile: s}
	if _, err := New(ok); err != nil {
		t.Fatal(err)
	}
	u, err := New(Config{Endpoint: "http://minio:9000", Bucket: "b", AccessKeyFile: a, SecretKeyFile: s, Prefix: "/vm/records/"})
	if err != nil || !u.cfg.Insecure || u.cfg.Endpoint != "minio:9000" || u.cfg.Prefix != "vm/records/" {
		t.Fatalf("scheme handling: %+v %v", u, err)
	}
	for name, bad := range map[string]Config{
		"no bucket":  {Endpoint: "m", AccessKeyFile: a, SecretKeyFile: s},
		"slash":      {Endpoint: "m", Bucket: "a/b", AccessKeyFile: a, SecretKeyFile: s},
		"no keys":    {Endpoint: "m", Bucket: "b"},
		"ftp":        {Endpoint: "ftp://m", Bucket: "b", AccessKeyFile: a, SecretKeyFile: s},
		"path":       {Endpoint: "https://m/x", Bucket: "b", AccessKeyFile: a, SecretKeyFile: s},
		"bad prefix": {Endpoint: "m", Bucket: "b", AccessKeyFile: a, SecretKeyFile: s, Prefix: "a/../b"},
	} {
		if _, err := New(bad); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestObjectKeys(t *testing.T) {
	a, s := keyFiles(t, "k", "v")
	u, _ := New(Config{Endpoint: "m", Bucket: "b", Prefix: "rec", AccessKeyFile: a, SecretKeyFile: s})
	if k, err := u.ObjectKey("2026/session.mp4"); err != nil || k != "rec/2026/session.mp4" {
		t.Fatalf("key = %q %v", k, err)
	}
	for _, bad := range []string{"", "/abs", "a//b", "../x", "a/./b", "a\\b"} {
		if _, err := u.ObjectKey(bad); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("%q accepted", bad)
		}
	}
}

// Against a real S3-compatible server when VM_AGENT_S3_ENDPOINT,
// VM_AGENT_S3_ACCESS_KEY and VM_AGENT_S3_SECRET_KEY are set (MinIO in CI).
func TestPutToRealServer(t *testing.T) {
	endpoint := os.Getenv("VM_AGENT_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set VM_AGENT_S3_ENDPOINT, VM_AGENT_S3_ACCESS_KEY and VM_AGENT_S3_SECRET_KEY")
	}
	access, secret := os.Getenv("VM_AGENT_S3_ACCESS_KEY"), os.Getenv("VM_AGENT_S3_SECRET_KEY")
	a, s := keyFiles(t, access, secret)
	bucket := "vm-agent-test"
	admin, err := minio.New(endpoint, &minio.Options{Creds: miniocreds.NewStaticV4(access, secret, "")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if exists, _ := admin.BucketExists(ctx, bucket); !exists {
		if err := admin.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	u, err := New(Config{Endpoint: endpoint, Bucket: bucket, Prefix: "it", AccessKeyFile: a, SecretKeyFile: s, Insecure: true})
	if err != nil {
		t.Fatal(err)
	}
	// 20 MiB forces a multipart upload.
	body := bytes.Repeat([]byte("0123456789abcdef"), 20<<16)
	key, _ := u.ObjectKey("big.mp4")
	res, err := u.Put(ctx, key, bytes.NewReader(body), int64(len(body)), "video/mp4")
	if err != nil || res.Size != int64(len(body)) || res.Key != "it/big.mp4" {
		t.Fatalf("put = %+v %v", res, err)
	}
	obj, err := admin.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(obj)
	if !bytes.Equal(got, body) {
		t.Fatalf("object has %d bytes", len(got))
	}
	// Rotated credentials are picked up without recreating the uploader.
	os.WriteFile(a, []byte("wrong\n"), 0o600)
	if _, err := u.Put(ctx, key, strings.NewReader("x"), 1, "video/mp4"); err == nil {
		t.Fatal("upload succeeded with rotated-out credentials")
	}
}
