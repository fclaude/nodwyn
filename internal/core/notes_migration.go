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

// aliasesChecked marks a project's directory as done with the search of old
// session records for the spellings of its folder (migrateNotes).
const aliasesChecked = ".aliases-checked"

// migrateNotes preserves notes indexed by a spelling used before folders had
// canonical identities. Aliases come from the requested folder and old session
// records, never repository-controlled files. The old files remain as backups;
// different notes require a person to reconcile them instead of losing either.
//
// The session records are searched once for each folder, not at every start
// and resume (1,500 logs took 850 ms of each): sessions record the canonical
// spelling now, so a record the search has seen is all there will be of the
// old ones, and a marker in the folder's directory says it has been done.
func migrateNotes(paths config.Paths, requested string) error {
	if paths.DataDir == "" {
		return nil
	}
	root := config.CanonicalPath(requested)
	target := notesFile(paths.DataDir, root)
	marker := filepath.Join(filepath.Dir(target), aliasesChecked)
	aliases := []string{requested}
	searched := false
	if _, err := os.Lstat(marker); errors.Is(err, os.ErrNotExist) && paths.SessionsDir != "" {
		more, err := sessionAliases(paths.SessionsDir, root)
		if err != nil {
			return err
		}
		for _, alias := range more {
			if !slices.Contains(aliases, alias) {
				aliases = append(aliases, alias)
			}
		}
		searched = true
	}
	if err := migrateFrom(paths.DataDir, aliases, target); err != nil {
		return err
	}
	if searched {
		// Best effort: without the marker the next start searches again.
		if err := os.MkdirAll(filepath.Dir(marker), 0o700); err == nil {
			_ = os.WriteFile(marker, nil, 0o600)
		}
	}
	return nil
}

// sessionAliases are the spellings of the folder at root that old sessions
// recorded as their repository.
func sessionAliases(sessionsDir, root string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(sessionsDir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	var aliases []string
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
	return aliases, nil
}

// migrateFrom moves the notes kept under the spellings in aliases to target.
func migrateFrom(dataDir string, aliases []string, target string) error {
	var sources []string
	var chosen []byte
	for _, alias := range aliases {
		abs, err := filepath.Abs(alias)
		if err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(filepath.Clean(abs)))
		old := filepath.Join(dataDir, "projects", hex.EncodeToString(sum[:8]), "notes.md")
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
