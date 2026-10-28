package integration

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/gesso/storage"
	"github.com/Elagoht/gesso/storage/s3"
	"github.com/Elagoht/gesso/storage/storagetest"
)

// A real S3-speaking server: GESSO_S3_ENDPOINT, with GESSO_S3_ACCESS_KEY,
// GESSO_S3_SECRET_KEY and GESSO_S3_BUCKET (a bucket that exists); CI runs
// Zenko CloudServer, which checks signatures. Without it the S3 tests skip,
// loudly.
func s3Options(t *testing.T) s3.Options {
	t.Helper()
	endpoint := os.Getenv("GESSO_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("S3 UNVERIFIED — GESSO_S3_ENDPOINT is not set")
	}
	return s3.Options{
		Endpoint: endpoint, Region: "us-east-1", Bucket: os.Getenv("GESSO_S3_BUCKET"),
		AccessKey: os.Getenv("GESSO_S3_ACCESS_KEY"), SecretKey: os.Getenv("GESSO_S3_SECRET_KEY"),
	}
}

func TestS3Storage(t *testing.T) {
	opts := s3Options(t)
	run := strconv.FormatInt(time.Now().UnixNano(), 36)
	n := 0
	storagetest.Run(t, func(t *testing.T) storage.Storage {
		n++
		o := opts
		o.Prefix = "gesso-test/" + run + "/" + strconv.Itoa(n) + "/"
		b, err := s3.New(o)
		if err != nil {
			t.Fatal(err)
		}
		return b
	})
}

func TestS3LargeAndRefused(t *testing.T) {
	opts := s3Options(t)
	opts.Prefix = "gesso-test/" + strconv.FormatInt(time.Now().UnixNano(), 36) + "/"
	b, err := s3.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	big := bytes.Repeat([]byte("0123456789abcdef"), 1<<20) // 16 MiB: spooled to a file
	if obj, err := b.Put(ctx, "big.bin", bytes.NewReader(big)); err != nil || obj.Size != int64(len(big)) {
		t.Fatalf("put: %+v %v", obj, err)
	}
	rc, obj, err := b.Open(ctx, "big.bin")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, big) || obj.Size != int64(len(big)) {
		t.Fatalf("got %d bytes, %+v", len(got), obj)
	}
	_ = b.Delete(ctx, "big.bin")
	paged := opts
	paged.PageSize = 2
	pb, _ := s3.New(paged)
	for i := range 5 {
		if _, err := pb.Put(ctx, "page/"+strconv.Itoa(i), strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	if list, err := pb.List(ctx, "page/"); err != nil || len(list) != 5 || list[4].Key != "page/4" {
		t.Fatalf("five keys over pages of two: %+v %v", list, err)
	}
	wrong := opts
	wrong.SecretKey = "not-the-secret"
	bw, _ := s3.New(wrong)
	if _, err := bw.Put(ctx, "x.txt", strings.NewReader("x")); err == nil || errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("a wrong secret was not refused: %v", err)
	}
	if _, err := b.Stat(ctx, "nothing/here.txt"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("a missing key: %v", err)
	}
}
