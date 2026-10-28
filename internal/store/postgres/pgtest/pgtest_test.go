package pgtest

import (
	"strings"
	"testing"
)

func TestImage(t *testing.T) {
	t.Parallel()
	image, err := Image()
	if err != nil {
		t.Fatal(err)
	}
	// 16 is the release tested for now (docs/configuration.md); another is
	// a decision, not an update.
	if !strings.HasPrefix(image, "docker.io/library/postgres:16.") {
		t.Errorf("the image is %s, want PostgreSQL 16", image)
	}
}
