package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/policyfile"
)

// migrateNotes preserves notes indexed by a spelling used before folders had
// canonical identities. Aliases come from the requested folder and old session
// records, never repository-controlled files. The old files remain as backups;
// different notes require a person to reconcile them instead of losing either.
func migrateNotes(paths config.Paths, requested string) error {
	if paths.DataDir == "" {
		return nil
	}
	root := config.CanonicalPath(requested)
	aliases := []string{requested}
	files, err := filepath.Glob(filepath.Join(paths.SessionsDir, "*.jsonl"))
	if err != nil {
		return err
	}
	if paths.SessionsDir != "" {
		for _, path := range files {
			f, err := os.Open(path)
			if err != nil {
				continue
			}
			decoder := json.NewDecoder(io.LimitReader(f, 1<<20))
			for {
				var event log.Event
				if decoder.Decode(&event) != nil {
					break
				}
				if event.Kind != log.KindSessionStart {
					continue
				}
				var start log.SessionStart
				if event.Decode(&start) == nil {
					alias := start.Repo
					if start.Worktree != nil {
						alias = start.Worktree.Repository
					}
					if config.CanonicalPath(alias) == root && !slices.Contains(aliases, alias) {
						aliases = append(aliases, alias)
					}
				}
				break
			}
			f.Close()
		}
	}
	target := notesFile(paths.DataDir, root)
	var sources []string
	var chosen []byte
	for _, alias := range aliases {
		abs, err := filepath.Abs(alias)
		if err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(filepath.Clean(abs)))
		old := filepath.Join(paths.DataDir, "projects", hex.EncodeToString(sum[:8]), "notes.md")
		if old == target || slices.Contains(sources, old) {
			continue
		}
		raw, err := policyfile.Read(old, 8<<10)
		if err != nil {
			return err
		}
		if raw == nil {
			continue
		}
		if chosen != nil && !bytes.Equal(chosen, raw) {
			return fmt.Errorf("project notes: aliases have different notes; reconcile %s and %s", sources[0], old)
		}
		chosen, sources = raw, append(sources, old)
	}
	if len(sources) == 0 {
		return nil
	}
	if err := policyfile.Update(context.Background(), target, func(current []byte) ([]byte, error) {
		if current != nil && !bytes.Equal(current, chosen) {
			return nil, fmt.Errorf("project notes: reconcile %s and %s before starting", sources[0], target)
		}
		return chosen, nil
	}); err != nil {
		return err
	}
	for _, old := range sources {
		backup := old + ".before-path-migration"
		if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("project notes: backup already exists or cannot be checked: %s", backup)
		}
		if err := os.Rename(old, backup); err != nil {
			return err
		}
	}
	return nil
}
