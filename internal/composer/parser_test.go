package composer

import (
	"strings"
	"testing"
)

// TestParseRejectsSanitizedServiceNameCollision guards the runtime-identity
// collision: SanitizeForImage folds non-alphanumeric runs to '-', so
// "web_1" and "web-1" both become "web-1" and the second container would
// fail with an opaque "name already in use".
func TestParseRejectsSanitizedServiceNameCollision(t *testing.T) {
	_, err := Parse([]byte(`
services:
  web_1:
    image: nginx:alpine
  web-1:
    image: nginx:alpine
`))
	if err == nil {
		t.Fatal("service names colliding after sanitization were accepted")
	}
	if !strings.Contains(err.Error(), "web_1") || !strings.Contains(err.Error(), "web-1") {
		t.Fatalf("error should name both services, got: %v", err)
	}
}

func TestParseAllowsDistinctServiceNames(t *testing.T) {
	if _, err := Parse([]byte(`
services:
  web:
    image: nginx:alpine
  api:
    image: nginx:alpine
  worker:
    image: nginx:alpine
`)); err != nil {
		t.Fatalf("distinct service names rejected: %v", err)
	}
}

// TestParseRejectsCaseOnlyCollision covers "Web" vs "web", which also fold to
// the same lowercase runtime name.
func TestParseRejectsCaseOnlyCollision(t *testing.T) {
	if _, err := Parse([]byte(`
services:
  Web:
    image: nginx:alpine
  web:
    image: nginx:alpine
`)); err == nil {
		t.Fatal("case-only service name collision was accepted")
	}
}
