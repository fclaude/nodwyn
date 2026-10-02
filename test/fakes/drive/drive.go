// Package drive is a fake of Google Drive's multipart upload endpoint
// (POST /upload/drive/v3/files?uploadType=multipart, Drive API v3) for
// tests. It reads the body as Drive documents it, a multipart/related
// request whose first part is the file's JSON metadata and whose second is
// its content, records what it saw, and answers with a new file's id the
// way Drive does. A request of any other shape is refused, as Drive would.
package drive

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
)

// Path is where the fake serves uploads, as Drive does.
const Path = "/upload/drive/v3/files"

// Upload is one upload the fake accepted.
type Upload struct {
	Auth      string         // the Authorization header
	Query     url.Values     // uploadType, fields
	Metadata  map[string]any // the first part, decoded
	MediaType string         // the second part's Content-Type
	Media     []byte         // the second part: the file
	ID        string         // the id the fake answered with
}

// Server is the fake.
type Server struct {
	*httptest.Server
	mu       sync.Mutex
	uploads  []Upload
	requests int
	refuse   int // when set, every upload is refused with this status
	n        int
}

// New starts a fake.
func New() *Server {
	s := &Server{}
	mux := http.NewServeMux()
	mux.HandleFunc(Path, s.handle)
	s.Server = httptest.NewServer(mux)
	return s
}

// UploadURL is the endpoint to configure (docs.google_upload_url).
func (s *Server) UploadURL() string { return s.URL + Path }

// Uploads returns the uploads accepted so far.
func (s *Server) Uploads() []Upload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Upload(nil), s.uploads...)
}

// Requests counts every request that reached the fake, accepted or not.
func (s *Server) Requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

// Refuse makes every later upload fail with status, as Drive answers a
// token without the scope it needs (403).
func (s *Server) Refuse(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuse = status
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests++
	refuse := s.refuse
	s.mu.Unlock()
	if r.Method != http.MethodPost || r.URL.Query().Get("uploadType") != "multipart" {
		driveError(w, http.StatusBadRequest, "fake drive: want POST ?uploadType=multipart")
		return
	}
	if refuse != 0 {
		driveError(w, refuse, "Insufficient Permission: Request had insufficient authentication scopes.")
		return
	}
	media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "multipart/related" || params["boundary"] == "" {
		driveError(w, http.StatusBadRequest, "fake drive: want a multipart/related body")
		return
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	up := Upload{Auth: r.Header.Get("Authorization"), Query: r.URL.Query()}
	meta, err := mr.NextPart()
	if err != nil {
		driveError(w, http.StatusBadRequest, "fake drive: no metadata part")
		return
	}
	if mt, _, _ := mime.ParseMediaType(meta.Header.Get("Content-Type")); mt != "application/json" {
		driveError(w, http.StatusBadRequest, "fake drive: the metadata part is not JSON")
		return
	}
	if err := json.NewDecoder(meta).Decode(&up.Metadata); err != nil {
		driveError(w, http.StatusBadRequest, "fake drive: bad metadata")
		return
	}
	file, err := mr.NextPart()
	if err != nil {
		driveError(w, http.StatusBadRequest, "fake drive: no media part")
		return
	}
	up.MediaType = file.Header.Get("Content-Type")
	if up.Media, err = io.ReadAll(file); err != nil {
		driveError(w, http.StatusBadRequest, "fake drive: bad media part")
		return
	}
	if _, err := mr.NextPart(); err != io.EOF {
		driveError(w, http.StatusBadRequest, "fake drive: more than two parts")
		return
	}
	s.mu.Lock()
	s.n++
	up.ID = fmt.Sprintf("fake-doc-%d", s.n)
	s.uploads = append(s.uploads, up)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"kind": "drive#file", "id": up.ID, "name": up.Metadata["name"], "mimeType": up.Metadata["mimeType"]})
}

// driveError answers as Drive does when it refuses.
func driveError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": status, "message": message}})
}
