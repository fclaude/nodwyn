// Package kids embeds nodwyn in an operator-owned, shell-free static app service.
package kids

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const maxExport = 512 << 20
const maxFile = 1 << 20
const maxProject = 16 << 20
const maxFiles = 256

var filePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)
var slugPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
var idPattern = regexp.MustCompile(`^[a-f0-9]{24}$`)

// Project's identity and physical root do not change when its public slug does.
type Project struct {
	ID      string `json:"id"`
	Slug    string `json:"slug"`
	Task    string `json:"task,omitempty"`
	Version uint64 `json:"version"`
}

// Mutation is the credential-free export contract. Blobs are immutable bytes;
// the record becomes visible only after its application is durable. Recovery
// rolls a prepared operation forward before accepting another mutation.
type Mutation struct {
	Version  int       `json:"version"`
	Seq      uint64    `json:"seq"`
	Time     time.Time `json:"time"`
	Project  string    `json:"project"`
	Slug     string    `json:"slug"`
	Task     string    `json:"task,omitempty"`
	Turn     string    `json:"turn,omitempty"`
	Outcome  string    `json:"outcome,omitempty"`
	Summary  string    `json:"summary,omitempty"`
	Op       string    `json:"op"`
	Path     string    `json:"path,omitempty"`
	Hash     string    `json:"hash,omitempty"`
	Projects []Project `json:"projects"`
}

// Store owns project authority and exports. Its directories are private mounts,
// never project files. The service is the sole writer, protected by a process
// lock in Run and this mutex within the process.
type Store struct {
	mu                sync.Mutex
	Workspace, Export string
	seq               uint64
	projects          []Project
	failed            error
}

func OpenStore(workspace, export string) (*Store, error) {
	s := &Store{Workspace: workspace, Export: export}
	for _, d := range []string{workspace, export, filepath.Join(export, "blobs"), filepath.Join(export, "records")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, err
		}
	}
	entries, err := os.ReadDir(filepath.Join(export, "records"))
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			return nil, errors.New("journal: unexpected directory")
		}
		name := fmt.Sprintf("%020d.json", s.seq+1)
		if e.Name() != name {
			return nil, errors.New("journal: sequence gap")
		}
		var m Mutation
		if err := readJSON(filepath.Join(export, "records", name), &m); err != nil {
			return nil, err
		}
		if m.Seq != s.seq+1 || m.Version != 1 {
			return nil, errors.New("journal: invalid sequence or version")
		}
		if err := s.validate(m); err != nil {
			return nil, err
		}
		s.seq, s.projects = m.Seq, m.Projects
	}
	var pending Mutation
	err = readJSON(filepath.Join(export, "pending.json"), &pending)
	if err == nil {
		if pending.Seq == s.seq { // Crash after committing but before removing the intent.
			committed, e := os.ReadFile(filepath.Join(export, "records", fmt.Sprintf("%020d.json", s.seq)))
			if e != nil {
				return nil, e
			}
			candidate, _ := json.Marshal(pending)
			if string(committed) != string(candidate) {
				return nil, errors.New("journal: conflicting completed intent")
			}
		} else if pending.Seq == s.seq+1 {
			if err = s.finish(pending); err != nil {
				return nil, err
			}
		} else {
			return nil, errors.New("journal: conflicting pending intent")
		}
		if err = os.Remove(filepath.Join(export, "pending.json")); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return s, nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// atomicFile syncs both the file and its directory before it acknowledges data.
func atomicFile(path string, b []byte) error   { return atomicMode(path, b, 0600) }
func atomicExport(path string, b []byte) error { return atomicMode(path, b, 0640) }
func atomicMode(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pending-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (s *Store) validate(m Mutation) error {
	if m.Version != 1 || !idPattern.MatchString(m.Project) || !slugPattern.MatchString(m.Slug) {
		return errors.New("journal: invalid project identity")
	}
	seenID, seenSlug := map[string]bool{}, map[string]bool{}
	if len(m.Projects) > 32 {
		return errors.New("too many projects")
	}
	for _, p := range m.Projects {
		if !idPattern.MatchString(p.ID) || !slugPattern.MatchString(p.Slug) || seenID[p.ID] || seenSlug[p.Slug] {
			return errors.New("journal: invalid registry")
		}
		seenID[p.ID], seenSlug[p.Slug] = true, true
	}
	switch m.Op {
	case "create", "write":
		if !validFile(m.Path) || len(m.Hash) != 64 {
			return errors.New("journal: invalid file")
		}
		b, err := os.ReadFile(filepath.Join(s.Export, "blobs", m.Hash))
		if err != nil {
			return err
		}
		hash := sha256.Sum256(b)
		if hex.EncodeToString(hash[:]) != m.Hash || len(b) > maxFile {
			return errors.New("journal: invalid blob")
		}
	case "rename", "delete", "task", "turn-end":
	default:
		return errors.New("journal: invalid operation")
	}
	return nil
}

func validFile(path string) bool {
	if path == "" || filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > 200 || len(strings.Split(path, "/")) > 7 {
		return false
	}
	for _, p := range strings.Split(path, "/") {
		if !filePattern.MatchString(p) || strings.HasPrefix(p, ".") || strings.EqualFold(p, "AGENTS.md") {
			return false
		}
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".html", ".css", ".js", ".json", ".txt", ".svg":
		return true
	}
	return false
}

func (s *Store) finish(m Mutation) error {
	if err := s.validate(m); err != nil {
		return err
	}
	switch m.Op {
	case "create", "write":
		b, err := os.ReadFile(filepath.Join(s.Export, "blobs", m.Hash))
		if err != nil {
			return err
		}
		if err = atomicFile(filepath.Join(s.Workspace, m.Project, m.Path), b); err != nil {
			return err
		}
	case "delete":
		if err := os.RemoveAll(filepath.Join(s.Workspace, m.Project)); err != nil {
			return err
		}
		d, err := os.Open(s.Workspace)
		if err != nil {
			return err
		}
		err = d.Sync()
		_ = d.Close()
		if err != nil {
			return err
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err = atomicExport(filepath.Join(s.Export, "records", fmt.Sprintf("%020d.json", m.Seq)), b); err != nil {
		return err
	}
	s.seq, s.projects = m.Seq, m.Projects
	return nil
}

func (s *Store) transact(m Mutation, content []byte) error {
	if s.failed != nil {
		return s.failed
	}
	used := int64(0)
	err := filepath.WalkDir(s.Export, func(_ string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() {
			st, e := d.Info()
			if e != nil {
				return e
			}
			used += st.Size()
		}
		return nil
	})
	if err != nil {
		return err
	}
	if used+int64(len(content))+(64<<10) > maxExport {
		return errors.New("history storage is full; ask a parent")
	}
	m.Version, m.Seq, m.Time = 1, s.seq+1, time.Now().UTC()
	if content != nil {
		if len(content) > maxFile {
			return errors.New("file is too large")
		}
		hash := sha256.Sum256(content)
		m.Hash = hex.EncodeToString(hash[:])
		if err = atomicExport(filepath.Join(s.Export, "blobs", m.Hash), content); err != nil {
			return err
		}
	}
	if err = s.validate(m); err != nil {
		return err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err = atomicExport(filepath.Join(s.Export, "pending.json"), b); err != nil {
		s.failed = err
		return err
	}
	if err = s.finish(m); err != nil {
		s.failed = err
		return err
	}
	if err = os.Remove(filepath.Join(s.Export, "pending.json")); err != nil {
		s.failed = err
		return err
	}
	return nil
}

func (s *Store) Projects() []Project {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Project{}, s.projects...)
}
func (s *Store) project(id string) (Project, int, error) {
	for i, p := range s.projects {
		if p.ID == id {
			return p, i, nil
		}
	}
	return Project{}, 0, errors.New("project not found")
}
func (s *Store) Create(slug string) (Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !slugPattern.MatchString(slug) || slug == "assets" || slug == "api" {
		return Project{}, errors.New("use a short name with letters, numbers and dashes")
	}
	for _, p := range s.projects {
		if p.Slug == slug {
			return Project{}, errors.New("that name is already used")
		}
	}
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return Project{}, err
	}
	p := Project{ID: hex.EncodeToString(raw[:]), Slug: slug, Version: 1}
	projects := append(append([]Project{}, s.projects...), p)
	m := Mutation{Project: p.ID, Slug: slug, Op: "create", Path: "index.html", Projects: projects}
	starter := []byte("<!doctype html><html lang=\"en\"><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><title>My app</title><style>body{font:1.2rem system-ui;max-width:50rem;margin:3rem auto;padding:1rem}</style><h1>My new app</h1><p>Tell your helper what you would like to make.</p></html>\n")
	if err := s.checkQuota(filepath.Join(s.Workspace, p.ID, "index.html"), len(starter)); err != nil {
		return Project{}, err
	}
	err := s.transact(m, starter)
	return p, err
}
func (s *Store) Change(id, op, slug, task string, version uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, i, err := s.project(id)
	if err != nil {
		return err
	}
	if p.Version != version {
		return errors.New("project changed; refresh and try again")
	}
	projects := append([]Project{}, s.projects...)
	switch op {
	case "rename":
		if !slugPattern.MatchString(slug) || slug == "assets" || slug == "api" {
			return errors.New("invalid name")
		}
		for _, q := range projects {
			if q.Slug == slug {
				return errors.New("that name is already used")
			}
		}
		projects[i].Slug = slug
	case "delete":
		projects = append(projects[:i], projects[i+1:]...)
	case "task":
		projects[i].Task = task
	default:
		return errors.New("invalid project operation")
	}
	if op != "delete" {
		projects[i].Version++
	}
	return s.transact(Mutation{Project: id, Slug: p.Slug, Task: task, Op: op, Projects: projects}, nil)
}
func (s *Store) Write(id, turn, path string, b []byte, _ os.FileMode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, i, err := s.project(id)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(filepath.Join(s.Workspace, id), path)
	if err != nil || !validFile(relative) {
		return errors.New("invalid project file")
	}
	if err = s.checkQuota(path, len(b)); err != nil {
		return err
	}
	projects := append([]Project{}, s.projects...)
	projects[i].Version++
	return s.transact(Mutation{Project: id, Slug: p.Slug, Task: p.Task, Turn: turn, Op: "write", Path: relative, Projects: projects}, b)
}
func (s *Store) EndTurn(id, turn, outcome string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, _, err := s.project(id)
	if err != nil {
		return err
	}
	return s.transact(Mutation{Project: id, Slug: p.Slug, Task: p.Task, Turn: turn, Outcome: outcome, Summary: "Project turn " + outcome + ".", Op: "turn-end", Projects: append([]Project{}, s.projects...)}, nil)
}

func sortedProjects(ps []Project) {
	sort.Slice(ps, func(i, j int) bool { return ps[i].Slug < ps[j].Slug })
}

// RecoverTurns closes the export of an interrupted writer after its prepared
// mutation has been recovered. Publication can then flush its durable history.
func (s *Store) RecoverTurns() error {
	open := map[string]string{}
	for seq := uint64(1); seq <= s.seq; seq++ {
		var m Mutation
		if err := readJSON(filepath.Join(s.Export, "records", fmt.Sprintf("%020d.json", seq)), &m); err != nil {
			return err
		}
		if m.Turn != "" {
			if m.Op == "turn-end" {
				delete(open, m.Project)
			} else {
				open[m.Project] = m.Turn
			}
		}
		if m.Op == "delete" {
			delete(open, m.Project)
		}
	}
	for id, turn := range open {
		if err := s.EndTurn(id, turn, "interrupted-recovery"); err != nil {
			return err
		}
	}
	return nil
}

// checkQuota matches the downstream preview/publication limits before an edit
// can be acknowledged. Two project writers share this store mutex, so neither
// can spend the other's remaining child-wide file/storage allowance.
func (s *Store) checkQuota(target string, size int) error {
	relative, err := filepath.Rel(s.Workspace, target)
	if err != nil {
		return err
	}
	id := strings.Split(relative, string(filepath.Separator))[0]
	files, bytes, projectFiles, projectBytes := 0, int64(0), 0, int64(0)
	found := false
	err = filepath.WalkDir(s.Workspace, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("links are unavailable")
		}
		if d.IsDir() {
			return nil
		}
		st, e := d.Info()
		if e != nil {
			return e
		}
		if !st.Mode().IsRegular() {
			return errors.New("special file")
		}
		n := st.Size()
		if path == target {
			n = int64(size)
			found = true
		}
		files++
		bytes += n
		rel, e := filepath.Rel(s.Workspace, path)
		if e != nil {
			return e
		}
		if strings.Split(rel, string(filepath.Separator))[0] == id {
			projectFiles++
			projectBytes += n
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !found {
		files++
		bytes += int64(size)
		projectFiles++
		projectBytes += int64(size)
	}
	if files > 512 || bytes > 32<<20 || projectFiles > maxFiles || projectBytes > maxProject {
		return errors.New("project storage is full; ask a parent")
	}
	return nil
}
