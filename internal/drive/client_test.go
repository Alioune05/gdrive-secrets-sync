package drive

import (
	"context"
	"testing"

	"github.com/aamoussou/gdrive-secrets-sync/internal/domain"
	"github.com/aamoussou/gdrive-secrets-sync/internal/syncer"
)

func TestEscapeQueryValue(t *testing.T) {
	cases := map[string]string{
		"simple":       "simple",
		"o'brien":      `o\'brien`,
		`back\slash`:   `back\\slash`,
		`both'\end`:    `both\'\\end`,
		"":             "",
		"no-specials/": "no-specials/",
	}
	for in, want := range cases {
		if got := escapeQueryValue(in); got != want {
			t.Errorf("escapeQueryValue(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatSize(t *testing.T) {
	cases := map[int64]string{
		0:    "?",
		1:    "1",
		1024: "1024",
	}
	for in, want := range cases {
		if got := formatSize(in); got != want {
			t.Errorf("formatSize(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestClientSatisfiesPort is a compile-time assertion (exercised by the test
// binary) that *Client implements the syncer.RemoteStore port. If the port or
// the client drift apart, this test file fails to compile.
func TestClientSatisfiesPort(t *testing.T) {
	var _ syncer.RemoteStore = (*Client)(nil)
	// Reference the domain type so the import is used even if the assertion
	// above is ever relaxed.
	_ = domain.RemoteFile{}
	_ = context.Background()
}
