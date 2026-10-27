package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fclaude/brote/scheme"
)

var managedName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)

// ManagedProfile is authority supplied by a trusted Go embedder, never by a
// writable project. Ordinary sessions leave it nil. The embedder owns the
// bounded provider and durable mutation sink, outside the model's file pin.
type ManagedProfile struct {
	Endpoint  string
	Model     string
	WriteFile func(string, []byte, os.FileMode) error
}

func (m *ManagedProfile) validate(o Options) error {
	c := o.Config
	// The profile's turn limits are the embedder's, every one of them set
	// and within its envelope; a task's own choice cannot widen them.
	if o.Limits == nil {
		return errors.New("managed: the profile's turn limits are required")
	}
	l := o.Limits.Limits
	if l.Wall <= 0 || l.Wall > int64(8*time.Minute) {
		return errors.New("managed: invalid wall limit")
	}
	if m.Endpoint == "" || m.Model == "" || o.Provider == nil || m.WriteFile == nil || !filepath.IsAbs(o.InitFile) {
		return errors.New("managed: explicit provider, policy and durable writes are required")
	}
	if o.Sandbox || o.Worktree || o.Keychain != nil || o.Containers != nil || o.JudgeApprove || len(c.Models) != 0 || len(c.Servers) != 0 || c.Experimental.Terminal || c.Control.Provider != "ollama" || c.Control.URL != m.Endpoint || c.Control.Model != m.Model || c.Control.Credential != "" || c.Control.NumCtx <= 0 || c.Control.NumCtx > 32768 || c.Control.MaxOutput <= 0 || c.Control.MaxOutput > 4096 {
		return errors.New("managed: configuration exceeds the immutable capability envelope")
	}
	if (o.Model.Name != "" && o.Model.Name != "control") || o.Model.Server != "" || o.Model.Model != "" || o.Judge != nil || o.Delegates != nil {
		return errors.New("managed: task authority cannot override the profile")
	}
	if l.ToolCalls <= 0 || l.ToolCalls > 40 || l.Actions <= 0 || l.Actions > 20 || l.Tokens <= 0 || l.Tokens > 200000 {
		return errors.New("managed: turn limits exceed the profile")
	}
	return nil
}

// Before is deliberately before guard approvals. Hidden or nested calls have
// exactly the same ceiling as advertised tools. A future primitive is denied
// until it is deliberately included here.
func (m *ManagedProfile) Before(root string, p *scheme.Prim, args []scheme.Value) error {
	if !p.Meta.Host {
		return nil
	}
	switch p.Name {
	case "fs-read", "fs-list", "fs-write", "fs-edit":
	default:
		return fmt.Errorf("managed: %s is unavailable", p.Name)
	}
	if len(args) == 0 || args[0].Kind != scheme.KString {
		return errors.New("managed: a project path is required")
	}
	path := args[0].S
	if filepath.IsAbs(path) {
		return errors.New("managed: use a relative project path")
	}
	if len(path) > 200 || len(strings.Split(path, "/")) > 7 {
		return errors.New("managed: path is too long")
	}
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return errors.New("managed: path leaves the project")
	}
	if clean == "." && p.Name == "fs-list" {
		return nil
	}
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if !managedName.MatchString(part) || strings.HasPrefix(part, ".") || strings.EqualFold(part, "AGENTS.md") {
			return errors.New("managed: metadata is unavailable")
		}
	}
	cur := root
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		st, err := os.Lstat(cur)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && (st.Mode()&os.ModeSymlink != 0 || (!st.IsDir() && !st.Mode().IsRegular())) {
			return errors.New("managed: links and special files are unavailable")
		}
	}
	if p.Name != "fs-list" {
		switch strings.ToLower(filepath.Ext(clean)) {
		case ".html", ".css", ".js", ".json", ".txt", ".svg":
		default:
			return errors.New("managed: only static app files are available")
		}
	}
	return nil
}
