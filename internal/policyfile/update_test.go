package policyfile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateDoesNotPublishFailedOrCanceledChange(t *testing.T) {
	for _, mode := range []string{"error", "cancel", "external-edit"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "policy.toml")
			if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rejected := errors.New("invalid policy")
			err := Update(ctx, path, func(raw []byte) ([]byte, error) {
				if string(raw) != "original" {
					t.Fatal("wrong input")
				}
				switch mode {
				case "error":
					return nil, rejected
				case "cancel":
					cancel()
				case "external-edit":
					if err := os.WriteFile(path, []byte("external edit"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return []byte("replacement"), nil
			})
			if err == nil {
				t.Fatal("published unapproved replacement")
			}
			if mode == "error" && !errors.Is(err, rejected) {
				t.Fatal(err)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			want := "original"
			if mode == "external-edit" {
				want = "external edit"
				if !strings.Contains(err.Error(), "changed") {
					t.Fatal(err)
				}
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != want {
				t.Fatalf("contents %q, error %v", got, err)
			}
			leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".policy-*"))
			if err != nil || len(leftovers) != 0 {
				t.Fatal("temporary file survived", leftovers, err)
			}
		})
	}
}
