// Document primitives (W5, ADR 22 answer 4): doc-export converts a
// workspace document with pandoc; google-doc-upload sends one to Google
// Drive, which makes it a Google Doc.
//
// doc-export runs pandoc twice, with fixed arguments, no shell, the
// session's scrubbed environment and shell-exec's timeout. The first run
// reads the source in pandoc's sandbox (--sandbox: no file but its input,
// no network), so an \input, an include or a remote image reads nothing,
// and writes pandoc's document tree as JSON. For a format that embeds
// images (.docx, .odt), Go then vets every image the tree names: a file
// inside the workspace that is not credential-shaped is kept, by its
// absolute path; anything else (a URL, a file outside, a .pem) is left out
// and its description kept. The second run writes the format from that
// tree. It cannot run sandboxed: a pandoc built without its data files
// embedded, as distributions build it, reads them from disk to write a
// .docx, which the sandbox refuses. It needs no sandbox either: the only
// files a writer reads for a tree are the images it embeds, and those are
// the ones Go kept. The source has the role source (read) and the target
// the role path (written), so the guard pins both, a read-only mount may
// supply the source, and the target is an irreversible write with its
// previous version kept.
//
// google-doc-upload is egress with a credential Go supplies, as an MCP
// server's key is: the guard judges the call as carrying the configured
// class to the upload endpoint's host (Meta.EgressClass), and the handler
// injects the stored credential through the session, which refreshes an
// OAuth2 token first and logs it. The document's bytes never become a
// Scheme value, so no checker sees them: the handler scans them itself (L1,
// through the session) and refuses a known secret shape whose class is not
// bound for that host, before anything is sent. Each upload is recorded as
// doc.upload: file, size, hash, destination, the document's id.
package prim

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/fclaude/nodwyn/internal/extract"
	"github.com/fclaude/nodwyn/internal/guard"
	nlog "github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/scheme"
)

const (
	maxExportSource = 16 << 20 // the largest document doc-export reads
	maxExportOutput = 64 << 20 // the most the second pandoc run may write
	// maxExportTree is the most the first run may write: pandoc's JSON tree,
	// which vetTree decodes into maps and slices, about 40 times its size.
	// 8 MiB takes some 300 MB for the moment it is held, and is a document of
	// several hundred KB; the 64 MiB the first run was allowed took gigabytes
	// from the daemon that hosts every session.
	maxExportTree  = 8 << 20
	maxPandocNotes = 2 << 10 // pandoc's warnings, as the result shows them
)

// docFormat is a pandoc reader or writer. A writer that embeds images
// reads the files they name; one that does not only refers to them.
type docFormat struct {
	pandoc, name string
	embeds       bool
}

// exportFrom maps a source's extension to pandoc's reader.
var exportFrom = map[string]docFormat{
	".md": {"markdown", "Markdown", false}, ".markdown": {"markdown", "Markdown", false},
	".tex": {"latex", "LaTeX", false}, ".latex": {"latex", "LaTeX", false},
}

// exportTo maps a target's extension to pandoc's writer.
var exportTo = map[string]docFormat{
	".docx": {"docx", "Word document", true},
	".odt":  {"odt", "OpenDocument text", true},
	".html": {"html5", "HTML page", false}, ".htm": {"html5", "HTML page", false},
}

// exportFormats returns the reader and writer for source and target, or
// why there is none.
func exportFormats(source, target string) (docFormat, docFormat, error) {
	from, ok := exportFrom[strings.ToLower(filepath.Ext(source))]
	if !ok {
		return docFormat{}, docFormat{}, fmt.Errorf("%s: doc-export converts Markdown (.md) or LaTeX (.tex)", source)
	}
	to, ok := exportTo[strings.ToLower(filepath.Ext(target))]
	if ok {
		return from, to, nil
	}
	if strings.EqualFold(filepath.Ext(target), ".pdf") {
		return docFormat{}, docFormat{}, fmt.Errorf("%s: doc-export does not write PDF, since a TeX engine reads and runs what a document asks and no sandbox covers it; build a PDF from LaTeX with shell-exec (latexmk), which asks unless you approved it", target)
	}
	return docFormat{}, docFormat{}, fmt.Errorf("%s: doc-export writes .docx, .odt or .html", target)
}

// pandocInstall says how to get a pandoc doc-export can run.
const pandocInstall = "doc-export needs pandoc 2.15 or later: install it with your package manager (dnf install pandoc, apt install pandoc, brew install pandoc) or from https://pandoc.org/installing.html"

// pandocMissing is the error when there is none.
const pandocMissing = "pandoc is not installed (there is no pandoc on PATH); " + pandocInstall

// pandocPath finds the pandoc doc-export runs.
func pandocPath(o Options) (string, error) {
	name := o.Pandoc
	if name == "" {
		name = "pandoc"
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", errors.New(pandocMissing)
	}
	return p, nil
}

func docExport(o Options, pin guard.RepoPin) *scheme.Prim {
	return &scheme.Prim{
		Name: "doc-export",
		Meta: scheme.Meta{
			Doc: "Convert a Markdown (.md) or LaTeX (.tex) file in the repository to a Word document (.docx), OpenDocument text (.odt) or HTML page (.html) with pandoc; the target's extension picks the format. In a .docx or .odt, images that are files in the repository are embedded; others (links, files outside) are left out, their descriptions kept; an .html page drops images linked from elsewhere; raw markup written into the source is kept as text. Replacing an existing target keeps its previous version.",
			Params: []scheme.Param{
				{Name: "source", Type: "string", Doc: "the .md or .tex file, relative to the repository root", Role: "source"},
				{Name: "target", Type: "string", Doc: "the file to write, relative to the repository root: .docx, .odt or .html", Role: "path"},
			},
			Host:         true,
			Irreversible: true,
			TaintSource:  scheme.TagFile,
			Preview:      func(args []scheme.Value) string { return previewExport(o, pin, args) },
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			src, err := resolve(pin, c, 0)
			if err != nil {
				return scheme.Value{}, err
			}
			dst, err := resolve(pin, c, 1)
			if err != nil {
				return scheme.Value{}, err
			}
			srcName, dstName := c.Args[0].S, c.Args[1].S
			from, to, err := exportFormats(srcName, dstName)
			if err != nil {
				return scheme.Value{}, err
			}
			content, err := readDocument(src, srcName, maxExportSource)
			if err != nil {
				return scheme.Value{}, err
			}
			prog, err := pandocPath(o)
			if err != nil {
				return scheme.Value{}, err
			}
			ctx, cancel := context.WithTimeout(c.Ctx, o.ShellTimeout)
			defer cancel()
			tree, notes1, err := runPandoc(ctx, c.Ctx, o, prog, content, maxExportTree, "--sandbox", "--from="+from.pandoc, "--to=json")
			if err != nil {
				return scheme.Value{}, err
			}
			title := ""
			if !to.embeds {
				title = strings.TrimSuffix(filepath.Base(dst), filepath.Ext(dst))
			}
			tree, dropped, raws, err := vetTree(tree, filepath.Dir(src), pin, denylist(o), title, to.embeds)
			if err != nil {
				return scheme.Value{}, err
			}
			doc, notes2, err := runPandoc(ctx, c.Ctx, o, prog, tree, maxExportOutput, "--from=json", "--to="+to.pandoc, "--standalone", "--output=-")
			if err != nil {
				return scheme.Value{}, err
			}
			if len(doc) == 0 {
				return scheme.Value{}, errors.New("pandoc wrote nothing")
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return scheme.Value{}, err
			}
			if err := o.Versions.Keep(dst); err != nil {
				return scheme.Value{}, fmt.Errorf("keeping the previous version failed, so nothing was written: %w", err)
			}
			if err := os.WriteFile(dst, doc, 0o644); err != nil {
				return scheme.Value{}, err
			}
			var b strings.Builder
			fmt.Fprintf(&b, "wrote %s (%s, %s) from %s", dstName, to.name, byteCount(len(doc)), srcName)
			if len(dropped) > 0 {
				fmt.Fprintf(&b, "\n%d image(s) left out, their descriptions kept: %s", len(dropped), strings.Join(dropped, "; "))
			}
			if len(raws) > 0 {
				fmt.Fprintf(&b, "\nraw content written into the source (%s) is shown as text, not interpreted", strings.Join(raws, ", "))
			}
			if notes := strings.TrimSpace(notes1 + notes2); notes != "" {
				b.WriteString("\npandoc said:\n" + clip(notes, maxPandocNotes))
			}
			return scheme.Str(b.String()), nil
		},
	}
}

// previewExport says what a doc-export would do, for the confirm.
func previewExport(o Options, pin guard.RepoPin, args []scheme.Value) string {
	if len(args) < 2 || args[0].Kind != scheme.KString || args[1].Kind != scheme.KString {
		return ""
	}
	src, dst := pin.Resolve(args[0].S), pin.Resolve(args[1].S)
	if src == "" || dst == "" {
		return "" // RepoPin denies it
	}
	from, to, err := exportFormats(args[0].S, args[1].S)
	if err != nil {
		return "the export would fail: " + err.Error()
	}
	size := "missing"
	if st, err := os.Stat(src); err == nil {
		size = byteCount(int(st.Size()))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s, %s) → %s (%s), converted by pandoc\n", args[0].S, from.name, size, args[1].S, to.name)
	if st, err := os.Stat(dst); err == nil {
		fmt.Fprintf(&b, "replaces %s (%s)", args[1].S, byteCount(int(st.Size())))
	} else {
		fmt.Fprintf(&b, "creates %s", args[1].S)
	}
	if _, err := pandocPath(o); err != nil {
		b.WriteString("\npandoc is not installed: the export will fail")
	}
	return b.String()
}

// readDocument reads a regular file of at most limit bytes.
func readDocument(abs, name string, limit int64) ([]byte, error) {
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a file", name)
	}
	if st.Size() > limit {
		return nil, fmt.Errorf("%s is %s; at most %s", name, byteCount(int(st.Size())), byteCount(int(limit)))
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("%s grew past %s while it was read", name, byteCount(int(limit)))
	}
	return content, nil
}

// runPandoc runs prog with args, input on stdin, and returns what it
// wrote, at most limit bytes of it, and its warnings. ctx carries the
// timeout; turn is the call's own context, which says whether the turn was
// aborted.
func runPandoc(ctx, turn context.Context, o Options, prog string, input []byte, limit int, args ...string) ([]byte, string, error) {
	cmd := exec.CommandContext(ctx, prog, args...)
	cmd.Dir = o.Root
	cmd.Env = childEnv(o)
	cmd.Stdin = bytes.NewReader(input)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	stdout := &bounded{n: limit}
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &capped{buf: &stderr, n: 64 << 10}
	runErr := cmd.Run()
	if turn.Err() != nil {
		return nil, "", turn.Err()
	}
	if ctx.Err() != nil {
		return nil, "", fmt.Errorf("pandoc timed out after %s", o.ShellTimeout)
	}
	notes := strings.TrimSpace(stderr.String())
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
	case errors.As(runErr, &exitErr):
		if strings.Contains(notes, "--sandbox") {
			return nil, "", errors.New("this pandoc has no --sandbox, which came in pandoc 2.15; " + pandocInstall)
		}
		return nil, "", fmt.Errorf("pandoc failed (exit %d): %s", exitErr.ExitCode(), clip(notes, maxPandocNotes))
	default:
		return nil, "", runErr
	}
	if stdout.over {
		return nil, "", fmt.Errorf("pandoc wrote more than %s: the document is too large to convert here", byteCount(limit))
	}
	if notes != "" {
		notes += "\n"
	}
	return stdout.buf.Bytes(), notes, nil
}

// clip shortens s to at most n bytes, at a character boundary, marking the
// cut.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// bounded keeps up to n bytes and notes whether more came. It keeps
// reading past n, so a writer never blocks on a full pipe.
type bounded struct {
	buf  bytes.Buffer
	n    int
	over bool
}

func (b *bounded) Write(p []byte) (int, error) {
	if b.over || b.buf.Len()+len(p) > b.n {
		b.over = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

// imageTypes are the extensions doc-export embeds.
var imageTypes = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true, ".bmp": true, ".tif": true, ".tiff": true, ".webp": true, ".emf": true, ".wmf": true}

// vetTree checks pandoc's JSON tree before the writer reads it.
//
// Raw content (RawBlock, RawInline: OpenXML, HTML, LaTeX written into the
// source) becomes literal code: the text stays, but no format interprets
// it, so a document cannot carry a field, a frame or a script that fetches
// something when it is opened.
//
// For a writer that embeds images (embeds), an image that is a file inside
// the workspace, not credential-shaped, and an image by its extension, is
// kept and named by its absolute path (a relative one is relative to base,
// the source's directory); a data: URI is kept, since it reads nothing.
// For one that only refers to them, as HTML's does, an image is kept unless
// it is a link elsewhere, which a page would load when opened. Any other
// image becomes a Span of its description, and is listed with why. A
// title, when given, becomes the page title of a tree that has none, which
// pandoc would otherwise fill with "-".
func vetTree(raw []byte, base string, pin guard.RepoPin, deny guard.PathDenylist, title string, embeds bool) ([]byte, []string, []string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree map[string]any
	if err := dec.Decode(&tree); err != nil {
		return nil, nil, nil, fmt.Errorf("pandoc's document tree: %w", err)
	}
	var dropped, raws []string
	var bad error
	var walk func(v any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			if (x["t"] == "RawBlock" || x["t"] == "RawInline") && len(asList(x["c"])) == 2 {
				c := asList(x["c"])
				format, _ := c[0].(string)
				text, _ := c[1].(string)
				raws = append(raws, format)
				kind := "Code"
				if x["t"] == "RawBlock" {
					kind = "CodeBlock"
				}
				return map[string]any{"t": kind, "c": []any{[]any{"", []any{}, []any{}}, text}}
			}
			if x["t"] == "Image" && !embeds {
				c := asList(x["c"])
				if len(c) == 3 {
					if target := asList(c[2]); len(target) == 2 {
						if src, _ := target[0].(string); remote(src) {
							dropped = append(dropped, src+" (a link elsewhere: a page would load it when opened)")
							return map[string]any{"t": "Span", "c": []any{c[0], walk(c[1])}}
						}
					}
				}
			}
			if x["t"] == "Image" && embeds {
				c, _ := x["c"].([]any)
				var target []any
				if len(c) == 3 {
					target, _ = c[2].([]any)
				}
				var src string
				if len(target) == 2 {
					src, _ = target[0].(string)
				}
				if src == "" {
					bad = errors.New("pandoc's document tree has an image nodwyn cannot read")
					return v
				}
				abs, why := vetImage(src, base, pin, deny)
				if why != "" {
					dropped = append(dropped, src+" ("+why+")")
					return map[string]any{"t": "Span", "c": []any{c[0], walk(c[1])}}
				}
				target[0] = abs
			}
			for k, e := range x {
				x[k] = walk(e)
			}
		case []any:
			for i, e := range x {
				x[i] = walk(e)
			}
		}
		return v
	}
	walk(tree)
	if bad != nil {
		return nil, nil, nil, bad
	}
	if meta, ok := tree["meta"].(map[string]any); ok && title != "" {
		if meta["title"] == nil && meta["pagetitle"] == nil {
			meta["pagetitle"] = map[string]any{"t": "MetaString", "c": title}
		}
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(tree); err != nil {
		return nil, nil, nil, err
	}
	return out.Bytes(), dropped, raws, nil
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// remote reports whether an image source names something elsewhere: a URL
// with a scheme other than data:, or a scheme-relative //host/path.
func remote(src string) bool {
	if strings.HasPrefix(src, "//") {
		return true
	}
	u, err := url.Parse(src)
	return err == nil && len(u.Scheme) > 1 && !strings.EqualFold(u.Scheme, "data")
}

// vetImage returns the absolute path to embed for an image's source, or
// why it is left out.
func vetImage(src, base string, pin guard.RepoPin, deny guard.PathDenylist) (string, string) {
	if strings.HasPrefix(strings.ToLower(src), "data:") {
		return src, ""
	}
	if u, err := url.Parse(src); err == nil && len(u.Scheme) > 1 {
		return "", "a link: only files in the repository are embedded"
	}
	path := src
	if p, err := url.PathUnescape(src); err == nil {
		path = p
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	path = filepath.Clean(path)
	switch {
	case pin.Resolve(path) == "":
		return "", "outside the repository"
	case deny.Denies(path) != "":
		return "", "a credential file"
	case !imageTypes[strings.ToLower(filepath.Ext(path))]:
		return "", "not an image"
	}
	if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
		return "", "no such file"
	}
	return path, ""
}

// DocsOptions configure google-doc-upload; the core fills them from
// config.Docs.
type DocsOptions struct {
	// GoogleClass is the credential class an upload carries; "" registers
	// no google-doc-upload.
	GoogleClass string
	// GoogleUploadURL is Drive's upload endpoint; its host is the
	// destination the guard judges.
	GoogleUploadURL string
}

// DocsClient supplies the host services google-doc-upload uses. Credential
// values remain behind injection: HandleForClass returns a handle, never a
// value. The core implements it.
type DocsClient interface {
	HandleForClass(class string) *scheme.Handle
	InjectHeader(id, name, prefix string, req *http.Request) error
	Do(req *http.Request) (*http.Response, error)
	Record(kind nlog.Kind, payload any) (nlog.Event, error)
	// CheckOutgoing judges text about to be sent to host by method as the
	// guard's L1 checker judges an egress payload (guard.TextSecret): nil
	// to send it, or why not.
	CheckOutgoing(text, host, method string) error
}

const (
	maxUploadBytes = 5 << 20 // Drive's limit for one multipart upload
	googleDocType  = "application/vnd.google-apps.document"
	googlePosition = "header:Authorization:Bearer"
	googleDocURL   = "https://docs.google.com/document/d/"
)

type uploadType struct{ media, name string }

// uploadTypes are the files Drive converts to a Google Doc.
var uploadTypes = map[string]uploadType{
	".docx": {"application/vnd.openxmlformats-officedocument.wordprocessingml.document", "Word document"},
	".odt":  {"application/vnd.oasis.opendocument.text", "OpenDocument text"},
	".html": {"text/html", "HTML page"}, ".htm": {"text/html", "HTML page"},
	".txt": {"text/plain", "plain text"},
}

// driveID is what a Drive file id looks like; anything else in a reply is
// not one.
var driveID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

// uploadHost is the host the guard judges: the configured endpoint's, so
// the log never names one host while the bytes go to another.
func uploadHost(o DocsOptions) string {
	if u, err := url.Parse(o.GoogleUploadURL); err == nil && u.Host != "" {
		return strings.ToLower(u.Hostname())
	}
	return strings.ToLower(o.GoogleUploadURL)
}

// uploadTitle is the title a call gives, or the file's name without its
// extension.
func uploadTitle(abs string, args []scheme.Value) string {
	if len(args) > 1 && args[1].Kind == scheme.KString && strings.TrimSpace(args[1].S) != "" {
		return args[1].S
	}
	return strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
}

func googleDocUpload(o DocsOptions, dc DocsClient, pin guard.RepoPin) *scheme.Prim {
	host := uploadHost(o)
	return &scheme.Prim{
		Name: "google-doc-upload",
		Meta: scheme.Meta{
			Doc: "Upload a document from the repository (.docx, .odt, .html or .txt, at most 5 MiB) to Google Drive, which makes it a new Google Doc; returns its id and URL. The file leaves the machine: every call asks, and a document holding something shaped like a key is refused. Convert Markdown or LaTeX with doc-export first.",
			Params: []scheme.Param{
				{Name: "path", Type: "string", Doc: "the document, relative to the repository root", Role: "source"},
				{Name: "title", Type: "string", Doc: "the Google Doc's title (default: the file's name without its extension)", Role: "payload", Optional: true},
			},
			Host:           true,
			Egress:         true,
			Irreversible:   true,
			Audited:        true,
			EgressHost:     host,
			EgressMethod:   http.MethodPost,
			EgressPosition: googlePosition,
			EgressClass:    o.GoogleClass,
			TaintSource:    scheme.TagHTTP,
			ConfirmReason:  "google-doc-upload sends a file out of this machine, to Google Drive at " + host,
			Preview:        func(args []scheme.Value) string { return previewUpload(pin, host, args) },
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			abs, err := resolve(pin, c, 0)
			if err != nil {
				return scheme.Value{}, err
			}
			name := c.Args[0].S
			kind, ok := uploadTypes[strings.ToLower(filepath.Ext(abs))]
			if !ok {
				return scheme.Value{}, fmt.Errorf("%s: google-doc-upload sends .docx, .odt, .html or .txt; convert Markdown or LaTeX with doc-export first", name)
			}
			content, err := readDocument(abs, name, maxUploadBytes)
			if err != nil {
				return scheme.Value{}, err
			}
			sum := sha256.Sum256(content)
			rec := nlog.DocUpload{Tool: c.Name, Origin: string(c.Origin), Path: abs, Type: kind.media, Bytes: len(content), SHA256: hex.EncodeToString(sum[:]), Host: host, Class: o.GoogleClass}
			fail := func(sent bool, err error) (scheme.Value, error) {
				rec.Sent, rec.Error = sent, err.Error()
				_, _ = dc.Record(nlog.KindDocUpload, rec)
				return scheme.Value{}, err
			}
			// The guard never saw these bytes: judge them as it judges a
			// payload, before anything leaves.
			text, err := extract.AllText(content, abs)
			if err != nil {
				return fail(false, fmt.Errorf("nothing was sent: %w", err))
			}
			if err := dc.CheckOutgoing(text, host, http.MethodPost); err != nil {
				return fail(false, fmt.Errorf("nothing was sent: %s: %w", name, err))
			}
			h := dc.HandleForClass(o.GoogleClass)
			if h == nil {
				return fail(false, fmt.Errorf("no %s credential is stored", o.GoogleClass))
			}
			title := uploadTitle(abs, c.Args)
			body, contentType, err := uploadBody(title, kind.media, content)
			if err != nil {
				return fail(false, err)
			}
			u, err := url.Parse(o.GoogleUploadURL)
			if err != nil {
				return fail(false, err)
			}
			u.RawQuery = url.Values{"uploadType": {"multipart"}, "fields": {"id,name"}}.Encode()
			ctx, cancel := context.WithTimeout(c.Ctx, 2*time.Minute)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), body)
			if err != nil {
				return fail(false, err)
			}
			req.Header.Set("Content-Type", contentType)
			if err := dc.InjectHeader(h.ID, "Authorization", "Bearer ", req); err != nil {
				return fail(false, fmt.Errorf("inject credential: %w", err))
			}
			resp, err := dc.Do(req)
			if err != nil {
				return fail(true, err)
			}
			defer resp.Body.Close()
			raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			if err != nil {
				return fail(true, fmt.Errorf("read the reply: %w", err))
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return fail(true, driveError(resp.Status, raw))
			}
			var file struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(raw, &file); err != nil || !driveID.MatchString(file.ID) {
				return fail(true, errors.New("the reply from Google Drive has no document id"))
			}
			rec.Sent, rec.OK, rec.ID, rec.URL = true, true, file.ID, googleDocURL+file.ID+"/edit"
			_, _ = dc.Record(nlog.KindDocUpload, rec)
			return scheme.List(
				scheme.Cons(scheme.Str("id"), scheme.Str(rec.ID)),
				scheme.Cons(scheme.Str("url"), scheme.Str(rec.URL)),
				scheme.Cons(scheme.Str("title"), scheme.Str(title)),
			), nil
		},
	}
}

// previewUpload says what an upload would send, and where, for the
// confirm. The prompt names the credential's class, destination and scopes
// besides.
func previewUpload(pin guard.RepoPin, host string, args []scheme.Value) string {
	if len(args) < 1 || args[0].Kind != scheme.KString {
		return ""
	}
	abs := pin.Resolve(args[0].S)
	if abs == "" {
		return "" // RepoPin denies it
	}
	kind, ok := uploadTypes[strings.ToLower(filepath.Ext(abs))]
	if !ok {
		return "the upload would fail: " + args[0].S + " is not a .docx, .odt, .html or .txt"
	}
	size := "missing"
	if st, err := os.Stat(abs); err == nil {
		size = byteCount(int(st.Size()))
	}
	return fmt.Sprintf("sends %s (%s, %s) to %s, where Google Drive makes it a new Google Doc titled %q", args[0].S, kind.name, size, host, uploadTitle(abs, args))
}

// uploadBody is a Drive multipart upload: the file's metadata, asking for
// a Google Doc, then its content.
func uploadBody(title, mediaType string, content []byte) (*bytes.Buffer, string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	meta, err := json.Marshal(map[string]string{"name": title, "mimeType": googleDocType})
	if err != nil {
		return nil, "", err
	}
	part, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"application/json; charset=UTF-8"}})
	if err != nil {
		return nil, "", err
	}
	part.Write(meta)
	if part, err = mw.CreatePart(textproto.MIMEHeader{"Content-Type": {mediaType}}); err != nil {
		return nil, "", err
	}
	part.Write(content)
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return &buf, "multipart/related; boundary=" + mw.Boundary(), nil
}

// driveError is Drive's refusal in its own words, shortened.
func driveError(status string, raw []byte) error {
	var reply struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := ""
	if json.Unmarshal(raw, &reply) == nil {
		msg = clip(reply.Error.Message, 300)
	}
	hint := ""
	if strings.HasPrefix(status, "401") || strings.HasPrefix(status, "403") {
		hint = " (check the credential's scopes: drive.file is enough)"
	}
	if msg == "" {
		return fmt.Errorf("the upload was refused by Google Drive: %s%s", status, hint)
	}
	return fmt.Errorf("the upload was refused by Google Drive: %s: %s%s", status, msg, hint)
}
