package artifacts

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
)

func TestS3StoreMinIOIntegration(t *testing.T) {
	if os.Getenv("AGENT_S3_INTEGRATION") != "1" {
		t.Skip("set AGENT_S3_INTEGRATION=1 to run against an S3-compatible store")
	}
	config := S3Config{Endpoint: os.Getenv("AGENT_S3_ENDPOINT"), Bucket: os.Getenv("AGENT_S3_BUCKET"), Prefix: "agent-platform-integration", AccessKeyFile: os.Getenv("AGENT_S3_ACCESS_FILE"), SecretKeyFile: os.Getenv("AGENT_S3_SECRET_FILE"), UseSSL: os.Getenv("AGENT_S3_INSECURE") != "1", RequestTimeout: 5 * time.Second}
	store, err := NewS3Store(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Health(); err != nil {
		t.Fatal(err)
	}
	client, err := store.client()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	exists, err := client.BucketExists(ctx, config.Bucket)
	if err != nil {
		t.Fatal(err)
	}
	created := false
	if !exists {
		if err := client.MakeBucket(ctx, config.Bucket, minio.MakeBucketOptions{}); err != nil {
			t.Fatal(err)
		}
		created = true
	}
	key := "roundtrip-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	content := []byte("s3-integration")
	if err := store.Put(key, content); err != nil {
		t.Fatal(err)
	}
	defer store.Delete(key)
	got, err := store.Get(key)
	if err != nil || string(got) != string(content) {
		t.Fatalf("round trip payload=%q err=%v", got, err)
	}
	if err := store.Delete(key); err != nil {
		t.Fatal(err)
	}
	if created {
		_ = client.RemoveBucket(ctx, config.Bucket)
	}
}
