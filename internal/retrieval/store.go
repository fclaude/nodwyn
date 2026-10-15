package retrieval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Store is a project's repository index, in nodwyn's data directory and
// never in the repository (ADR 50 §4). A file's chunks are kept once, under
// the hash of its redacted text, so every working tree of the project that
// holds the same text shares them; each working tree has a manifest of its
// files. All of it is derived from the files: what is missing or does not
// read is indexed again, and deleting the directory loses nothing.
type Store struct{ Dir string }

// FileState is what a manifest keeps of one file: enough to tell that it
// changed without reading it, and the hash its chunks are stored under.
type FileState struct {
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime_ns"`
	Inode uint64 `json:"inode"`
	Hash  string `json:"hash"`
}

// Manifest is one working tree's index: its root, its files by their path
// from the root (with /), and the directories index-update was given,
// indexed whole whatever git ignores.
type Manifest struct {
	Version int                  `json:"version"`
	Root    string               `json:"root"`
	Files   map[string]FileState `json:"files"`
	Extra   []string             `json:"extra,omitempty"`
}

// ManifestPath is where the manifest of the tree at root lives.
func (s Store) ManifestPath(root string) string {
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(s.Dir, "trees", hex.EncodeToString(sum[:8])+".json")
}

// ParseManifest reads a manifest. One that does not parse, or is of
// another version or another root, is an empty one: indexed again.
func ParseManifest(raw []byte, root string) Manifest {
	var m Manifest
	if json.Unmarshal(raw, &m) != nil || m.Version != ChunkVersion || m.Root != root || m.Files == nil {
		return Manifest{Version: ChunkVersion, Root: root, Files: map[string]FileState{}}
	}
	return m
}

// Encode is the manifest as it is written.
func (m Manifest) Encode() ([]byte, error) { return json.Marshal(m) }

// Hash is the key a text's chunks are stored under.
func Hash(text string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("chunks %d\x00%s", ChunkVersion, text)))
	return hex.EncodeToString(sum[:])
}

func (s Store) chunkPath(hash string) string {
	return filepath.Join(s.Dir, "files", hash[:2], hash+".json")
}

// Chunks returns the chunks stored under hash; false when there are none
// or they do not read.
func (s Store) Chunks(hash string) ([]Chunk, bool) {
	if len(hash) < 2 {
		return nil, false
	}
	raw, err := os.ReadFile(s.chunkPath(hash))
	if err != nil {
		return nil, false
	}
	var chunks []Chunk
	if json.Unmarshal(raw, &chunks) != nil {
		return nil, false
	}
	return chunks, true
}

// Has reports whether chunks are stored under hash.
func (s Store) Has(hash string) bool {
	_, err := os.Stat(s.chunkPath(hash))
	return err == nil
}

// Put stores a text's chunks under its hash: written to a temporary file
// and renamed into place, so a reader never sees half of one, and two
// writers of the same text write the same bytes.
func (s Store) Put(hash string, chunks []Chunk) error {
	raw, err := json.Marshal(chunks)
	if err != nil {
		return err
	}
	path := s.chunkPath(hash)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".chunks-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Collect removes the manifests of trees that are gone (a removed
// worktree) and the stored chunks that no manifest names and nothing
// wrote since before. It is called while the caller holds no manifest's
// lock, so a chunk written for a manifest not yet published is younger
// than before and stays.
func (s Store) Collect(before time.Time) error {
	named := map[string]bool{}
	manifests, _ := filepath.Glob(filepath.Join(s.Dir, "trees", "*.json"))
	for _, path := range manifests {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var m Manifest
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		if _, err := os.Stat(m.Root); errors.Is(err, fs.ErrNotExist) {
			_ = os.Remove(path)
			_ = os.Remove(path + ".lock")
			continue
		}
		for _, f := range m.Files {
			named[f.Hash] = true
		}
	}
	return filepath.WalkDir(filepath.Join(s.Dir, "files"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		hash := strings.TrimSuffix(d.Name(), ".json")
		if named[hash] {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().Before(before) {
			_ = os.Remove(path)
		}
		return nil
	})
}
