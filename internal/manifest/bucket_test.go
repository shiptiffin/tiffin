package manifest

import (
	"strings"
	"testing"
)

// Bucket S3 names ("<project>--<bucket>") are checked before anything is
// committed: they can't collide across projects, must fit S3's 63
// characters, and must not end in a suffix S3 reserves.
func TestBucketS3Names(t *testing.T) {
	if S3BucketName("shop", "a-b") == S3BucketName("shop-a", "b") {
		t.Fatal("bucket names collide across projects")
	}
	for _, c := range []struct{ doc, err string }{
		{`{"project":"shop","services":{"storage":{"buckets":{"media":{}}}}}`, ""},
		{`{"project":"shop","services":{"storage":{"buckets":{"x-s3":{}}}}}`, `ends in "--x-s3"`},
		{`{"project":"shop","services":{"storage":{"buckets":{"` + strings.Repeat("b", 40) + `":{}}}}}`, ""},
		{`{"project":"` + strings.Repeat("p", 30) + `","services":{"storage":{"buckets":{"` + strings.Repeat("b", 40) + `":{}}}}}`, "S3 allows 63"},
	} {
		m, err := Parse([]byte(c.doc))
		if err == nil {
			err = Validate(m)
		}
		if (c.err == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.err)) {
			t.Errorf("%s: %v", c.doc, err)
		}
	}
}
