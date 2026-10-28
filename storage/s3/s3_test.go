package s3

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// AWS's own worked example of Signature Version 4: GET Object, with a range.
// https://docs.aws.amazon.com/AmazonS3/latest/API/sig-v4-header-based-auth.html
func TestSignMatchesAWSExample(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	req.Header.Set("Range", "bytes=0-9")
	req.Header.Set("X-Amz-Content-Sha256", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	sign(req, "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "us-east-1",
		time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, " +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, " +
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

func TestEscaping(t *testing.T) {
	if got := escapePath("/bucket/a b/ç+=.png"); got != "/bucket/a%20b/%C3%A7%2B%3D.png" {
		t.Fatal(got)
	}
	if got := canonicalQuery(map[string][]string{"prefix": {"a/b c"}, "list-type": {"2"}}); got != "list-type=2&prefix=a%2Fb%20c" {
		t.Fatal(got)
	}
}

func TestNewRefuses(t *testing.T) {
	for _, o := range []Options{{}, {Endpoint: "s3.example.com", Region: "r", Bucket: "b", AccessKey: "a", SecretKey: "s"}} {
		if _, err := New(o); err == nil || !strings.HasPrefix(err.Error(), "s3:") {
			t.Errorf("%+v: %v", o, err)
		}
	}
}
