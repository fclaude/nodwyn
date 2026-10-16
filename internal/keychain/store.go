// Package keychain is the encrypted credential store (ADR 5). Secrets are
// credentials, not content (SPEC invariant 3): the harness holds opaque
// handles, and the value lives here, behind an AEAD. A value is never a
// scheme.Value and never leaves this package except through the Inject*
// sink functions, which write it into an outgoing request at a named
// position, or into the environment of a container runtime at a named
// variable. The value type has no String() and no accessor at all.
//
// File format (all little-endian):
//
//	magic (8) | kdf: time(4) memory(4) threads(1) | salt (16) | nonce (24) | ciphertext
//
// The KDF parameters are in the cleartext header because they are needed to
// derive the key that opens the ciphertext. The AEAD is XChaCha20-Poly1305
// (24-byte nonce, 16-byte tag); the key is argon2id of the passphrase and
// salt. Losing the passphrase loses the credentials; they are re-registerable
// (ADR 5). A keychain may have no passphrase, for people who would rather
// not type one: it is sealed under the empty passphrase, so the file is as
// safe as its permissions and no safer, and it opens without asking
// (OpenWithoutPassphrase). The passphrase can be set, changed or removed
// later (SetPassphrase).
//
// A credential has a kind (ADR 24): static, one value, or oauth2, a token
// set that expires and is refreshed here, inside the package, just before it
// is injected (oauth.go). Several processes may hold the same keychain open
// (the daemon, a CLI command); every change takes a lock on a sibling file
// and starts from what is on disk, and reads pick up a file another process
// wrote, so no process writes back a token another one already rotated.
package keychain

import (
	"bytes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"

	"github.com/fclaude/nodwyn/internal/scheme"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

var (
	// The format marker keeps nodwyn's former name (ADR 31): renaming it
	// would make every existing keychain unreadable.
	magic       = []byte("NIBBKC01")
	errNotFound = errors.New("keychain: no such credential")
	errWrong    = errors.New("keychain: wrong passphrase or corrupted file")

	// ErrNeedsPassphrase is OpenWithoutPassphrase's answer for a keychain
	// that has a passphrase.
	ErrNeedsPassphrase = errors.New("keychain: it has a passphrase")
)

const (
	keyLen    = 32
	saltLen   = 16
	headerLen = 8 + 4 + 4 + 1 + saltLen // magic + KDF params + salt
)

// value is the opaque secret. It is unexported and has no methods that yield
// the bytes; only the Inject* sink functions read it (ADR 5).
type value []byte

// The kinds of credential (ADR 24).
const (
	KindStatic = "static"
	KindOAuth2 = "oauth2"
)

// entry is one stored credential. value never appears in the handle that
// Scheme sees. A static entry holds its value; an oauth2 entry holds its
// token set, whose access token is what is injected.
type entry struct {
	class   string
	created time.Time
	value   value
	oauth   *tokenSet
}

func (e entry) kind() string {
	if e.oauth != nil {
		return KindOAuth2
	}
	return KindStatic
}

// wireDoc is the plaintext the AEAD protects: the metadata plus the base64
// values. It is never written to disk in this form. Version 1 holds only
// static keys; a keychain with an oauth2 entry is written as version 2, so
// a binary that predates them refuses it instead of rewriting it without
// its token sets. Version 3 additionally preserves the OpenAI host identity
// even when its first sign-in was cancelled.
type wireDoc struct {
	Version    int     `json:"version"`
	OpenAIHost string  `json:"openai_host,omitempty"`
	Entries    []wireE `json:"entries"`
}

type wireE struct {
	ID      string    `json:"id"`
	Class   string    `json:"class"`
	Created time.Time `json:"created"`
	Value   string    `json:"value,omitempty"` // base64, static
	Kind    string    `json:"kind,omitempty"`  // "" is static
	OAuth2  *tokenSet `json:"oauth2,omitempty"`
}

// Store is an opened keychain. The AEAD and KDF parameters are kept so
// entries can be re-sealed when one is added; the passphrase itself is not
// retained.
type Store struct {
	path         string
	openAIHostID string
	aead         cipher.AEAD
	kdf          KDFParams
	salt         []byte
	open         bool // sealed under the empty passphrase: it has none
	entries      map[string]entry
	order        []string // insertion order, for deterministic sealing
	stamp        stamp    // the file as this process last read or wrote it
	// pending holds a refreshed token set whose save failed, by credential
	// id: the provider may already have retired the refresh token the file
	// still holds, so a reload puts the set back until a save publishes it.
	pending map[string]pendingToken
	mu      sync.Mutex

	// client and now are the HTTP client and clock of OAuth2 refresh,
	// revocation and acquisition (ADR 24); tests replace them.
	client *http.Client
	now    func() time.Time
}

// pendingToken is a token set the file does not hold yet, and the refresh
// token the file has instead, which tells a reload whether the file is
// still the version the set came from.
type pendingToken struct {
	from string
	set  tokenSet
}

// stamp identifies a version of the keychain file, so a read can tell that
// another process has written it since.
type stamp struct {
	size int64
	mod  time.Time
}

func stampOf(path string) stamp {
	info, err := os.Stat(path)
	if err != nil {
		return stamp{}
	}
	return stamp{size: info.Size(), mod: info.ModTime()}
}

// KDFParams are the argon2id parameters used to derive the keychain key.
type KDFParams struct {
	Time    uint32
	Memory  uint32 // KiB
	Threads uint8
}

// DefaultKDF is the production parameter set (OWASP-recommended memory-hard
// values for passphrase-based KDFs).
var DefaultKDF = KDFParams{Time: 2, Memory: 19 * 1024, Threads: 1}

// FastKDF is a low-cost parameter set for tests: milliseconds, not seconds.
// It is NOT for production.
var FastKDF = KDFParams{Time: 1, Memory: 8 * 1024, Threads: 1}

// The header is unauthenticated until after derivation, so it must not be
// allowed to request unbounded work. These limits include both supported sets.
const maxFileSize = 16 << 20

func (p KDFParams) validate() error {
	if p.Time < 1 || p.Time > 10 || p.Threads < 1 || p.Threads > 16 || p.Memory < 8*uint32(p.Threads) || p.Memory > 256*1024 || uint64(p.Time)*uint64(p.Memory) > 512*1024 {
		return errors.New("keychain: KDF parameters exceed supported bounds")
	}
	return nil
}

func readSealed(path string) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxFileSize {
		return nil, errors.New("keychain: expected a regular file of at most 16 MiB")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if len(raw) > maxFileSize {
		return nil, errors.New("keychain: file exceeds 16 MiB")
	}
	return raw, err
}

func derive(p KDFParams, passphrase, salt []byte) []byte {
	return argon2.IDKey(passphrase, salt, p.Time, p.Memory, p.Threads, keyLen)
}

func random(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failed: not recoverable
	}
	return b
}

// Create makes a new, empty keychain at path using the given KDF parameters
// (pass DefaultKDF for production, FastKDF in tests). An empty passphrase
// makes a keychain without one. A keychain already at path is never
// replaced: under the keychain's lock, so that two creates at once (the
// console's and nodwyn keychain init) cannot both pass a check and the
// second replace the first with every key added in between.
func Create(path string, kdf KDFParams, passphrase []byte) (*Store, error) {
	if err := kdf.validate(); err != nil {
		return nil, err
	}
	salt := random(saltLen)
	st := openedStore(path, kdf, salt)
	unlock, err := st.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	if _, err := os.Lstat(path); err == nil {
		return nil, fmt.Errorf("keychain: %s already exists: %w", path, os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(derive(kdf, passphrase, salt))
	if err != nil {
		return nil, err
	}
	st.aead, st.open = aead, len(passphrase) == 0
	if err := st.save(); err != nil {
		return nil, err
	}
	return st, nil
}

func openedStore(path string, kdf KDFParams, salt []byte) *Store {
	return &Store{path: path, kdf: kdf, salt: salt, entries: map[string]entry{}, client: defaultClient, now: time.Now}
}

// sealed is a keychain file split into its parts.
type sealed struct {
	kdf   KDFParams
	salt  []byte
	nonce []byte
	ct    []byte
}

func split(raw []byte) (sealed, error) {
	if len(raw) > maxFileSize {
		return sealed{}, errors.New("keychain: file exceeds 16 MiB")
	}
	if len(raw) < headerLen+chacha20poly1305.NonceSizeX+chacha20poly1305.Overhead {
		return sealed{}, errors.New("keychain: file too short")
	}
	if !bytes.Equal(raw[:len(magic)], magic) {
		return sealed{}, errors.New("keychain: not a keychain file (bad magic)")
	}
	off := len(magic)
	kdf := KDFParams{
		Time:    binary.LittleEndian.Uint32(raw[off : off+4]),
		Memory:  binary.LittleEndian.Uint32(raw[off+4 : off+8]),
		Threads: raw[off+8],
	}
	if err := kdf.validate(); err != nil {
		return sealed{}, err
	}
	off += 9
	salt := raw[off : off+saltLen]
	off += saltLen
	nonce := raw[off : off+chacha20poly1305.NonceSizeX]
	off += chacha20poly1305.NonceSizeX
	return sealed{kdf: kdf, salt: salt, nonce: nonce, ct: raw[off:]}, nil
}

// Open reads and decrypts the keychain at path. A wrong passphrase is an
// AEAD authentication failure, reported as a distinct error.
func Open(path string, passphrase []byte) (*Store, error) {
	stamp := stampOf(path)
	raw, err := readSealed(path)
	if err != nil {
		return nil, err
	}
	f, err := split(raw)
	if err != nil {
		return nil, err
	}
	key := derive(f.kdf, passphrase, f.salt)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	pt, err := aead.Open(nil, f.nonce, f.ct, magic)
	if err != nil {
		return nil, errWrong
	}
	st := openedStore(path, f.kdf, f.salt)
	st.aead, st.open = aead, len(passphrase) == 0
	if err := st.decode(pt); err != nil {
		return nil, err
	}
	st.stamp = stamp
	return st, nil
}

// OpenWithoutPassphrase opens a keychain that has no passphrase. One that
// has a passphrase is ErrNeedsPassphrase; trying costs one key derivation.
func OpenWithoutPassphrase(path string) (*Store, error) {
	st, err := Open(path, nil)
	if errors.Is(err, errWrong) {
		return nil, ErrNeedsPassphrase
	}
	return st, err
}

// HasPassphrase reports whether the keychain needs a passphrase to open.
func (st *Store) HasPassphrase() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return !st.open
}

// SetPassphrase seals the keychain under a new passphrase, with a new salt;
// an empty one leaves it without a passphrase. Another process that holds
// the keychain open can no longer change it, and must open it again.
func (st *Store) SetPassphrase(passphrase []byte) error {
	unlock, err := st.lock()
	if err != nil {
		return err
	}
	defer unlock()
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := st.reloadLocked(); err != nil {
		return err
	}
	salt := random(saltLen)
	aead, err := chacha20poly1305.NewX(derive(st.kdf, passphrase, salt))
	if err != nil {
		return err
	}
	oldAEAD, oldSalt, oldOpen := st.aead, st.salt, st.open
	st.aead, st.salt, st.open = aead, salt, len(passphrase) == 0
	if err := st.save(); err != nil {
		var published *publishedError
		if !errors.As(err, &published) {
			st.aead, st.salt, st.open = oldAEAD, oldSalt, oldOpen
		}
		return err
	}
	return nil
}

// Stale reports whether the file was sealed again since this store opened
// it (another process changed the passphrase, or replaced the keychain): it
// can no longer change the keychain or refresh a token until Reopen, though
// the keys in memory still inject.
func (st *Store) Stale() bool {
	raw, err := readSealed(st.path)
	if err != nil {
		return false
	}
	f, err := split(raw)
	if err != nil {
		return false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return !bytes.Equal(f.salt, st.salt)
}

// Reopen opens the file again with passphrase and makes st what it holds,
// in place, so everything that shares st carries on with the new key.
func (st *Store) Reopen(passphrase []byte) error {
	fresh, err := Open(st.path, passphrase)
	if err != nil {
		return err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.aead, st.kdf, st.salt, st.open = fresh.aead, fresh.kdf, fresh.salt, fresh.open
	st.entries, st.order, st.openAIHostID, st.stamp = fresh.entries, fresh.order, fresh.openAIHostID, fresh.stamp
	return nil
}

// reloadLocked replaces the entries with what the file holds now. The
// caller holds st.mu.
func (st *Store) reloadLocked() error {
	stamp := stampOf(st.path)
	raw, err := readSealed(st.path)
	if err != nil {
		return fmt.Errorf("keychain: reread: %w", err)
	}
	f, err := split(raw)
	if err != nil {
		return err
	}
	if !bytes.Equal(f.salt, st.salt) {
		return errors.New("keychain: the keychain was replaced or its passphrase changed; unlock it again")
	}
	pt, err := st.aead.Open(nil, f.nonce, f.ct, magic)
	if err != nil {
		return errors.New("keychain: the file no longer opens with this passphrase; unlock it again")
	}
	entries, order, host := st.entries, st.order, st.openAIHostID
	st.entries, st.order = map[string]entry{}, nil
	if err := st.decode(pt); err != nil {
		st.entries, st.order, st.openAIHostID = entries, order, host
		return err
	}
	st.stamp = stamp
	st.restorePending()
	return nil
}

// restorePending puts back each refreshed token set the file still lacks.
// A file whose refresh token is not the one the set replaced was written
// by someone else since (a sign-in again, another process's refresh), and
// wins.
func (st *Store) restorePending() {
	for id, p := range st.pending {
		e, ok := st.entries[id]
		if !ok || e.oauth == nil || e.oauth.RefreshToken != p.from {
			delete(st.pending, id)
			continue
		}
		t := p.set
		e.oauth = &t
		st.entries[id] = e
	}
}

// freshLocked rereads the file if another process wrote it since this one
// last did. A file that cannot be read keeps what is in memory. The caller
// holds st.mu.
func (st *Store) freshLocked() {
	if stampOf(st.path) != st.stamp {
		_ = st.reloadLocked()
	}
}

// change applies one change to the keychain as it is on disk: it takes the
// sibling lock shared by every nodwyn process, rereads the file, applies
// change and writes the result, so no process overwrites another's change.
func (st *Store) change(apply func() error) error {
	unlock, err := st.lock()
	if err != nil {
		return err
	}
	defer unlock()
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := st.reloadLocked(); err != nil {
		return err
	}
	entries, order, host := maps.Clone(st.entries), append([]string(nil), st.order...), st.openAIHostID
	err = apply()
	if err == nil {
		err = st.save()
	}
	var published *publishedError
	if err != nil && !errors.As(err, &published) {
		st.entries, st.order, st.openAIHostID = entries, order, host
	}
	return err
}

// lock takes the exclusive advisory lock on the keychain's sibling lock
// file. It serializes changes across processes, and refreshes across
// goroutines too, since each call opens its own descriptor.
func (st *Store) lock() (func(), error) {
	fd, err := syscall.Open(st.path+".lock", syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("keychain: lock: %w", err)
	}
	for {
		err = syscall.Flock(fd, syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("keychain: lock: %w", err)
	}
	return func() {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		_ = syscall.Close(fd)
	}, nil
}

// Add stores a new credential of the given class and returns its opaque
// handle. It persists the store. The value is kept in memory, unexported,
// for the Inject* sinks; it is never returned.
func (st *Store) Add(class string, secret []byte) (*scheme.Handle, error) {
	return st.add(entry{class: class, created: time.Now().UTC(), value: value(append([]byte(nil), secret...))})
}

// classPattern is what a credential's class may be: it is written into
// bindings.toml and config.toml, and typed by people.
var classPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// BadClass says what a class may be, for whoever refused one.
const BadClass = "A class is letters, digits, dots, dashes or underscores, such as github-token"

// ValidClass reports whether class may name a credential.
func ValidClass(class string) bool { return classPattern.MatchString(class) }

// newClassLocked refuses a class that is not valid or that a credential
// has: a lookup by class finds the first, so a second would never be sent.
// The caller holds st.mu.
func (st *Store) newClassLocked(class string) error {
	if !ValidClass(class) {
		return errors.New("keychain: " + BadClass)
	}
	for _, e := range st.entries {
		if e.class == class {
			return fmt.Errorf("keychain: a credential of class %s already exists; replace its value, or authorize it again", class)
		}
	}
	return nil
}

func (st *Store) add(e entry) (*scheme.Handle, error) {
	id := fmt.Sprintf("%x", random(8))
	err := st.change(func() error {
		if err := st.newClassLocked(e.class); err != nil {
			return err
		}
		if _, dup := st.entries[id]; dup {
			return errors.New("keychain: id collision")
		}
		st.entries[id] = e
		st.order = append(st.order, id)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &scheme.Handle{Class: e.class, ID: id}, nil
}

// Replace gives the static credential id a new value, keeping its id and
// class, so the bindings and settings that name the class keep working: a
// key rotated where it was issued.
func (st *Store) Replace(id string, secret []byte) error {
	return st.change(func() error {
		e, ok := st.entries[id]
		if !ok {
			return errNotFound
		}
		if e.oauth != nil {
			return fmt.Errorf("keychain: %s is an oauth2 credential; authorize it again instead", e.class)
		}
		e.value = value(append([]byte(nil), secret...))
		st.entries[id] = e
		return nil
	})
}

// Delete removes a credential. An oauth2 credential's tokens stay valid at
// the provider; Revoke revokes them first.
func (st *Store) Delete(id string) error {
	return st.change(func() error {
		if _, ok := st.entries[id]; !ok {
			return errNotFound
		}
		delete(st.entries, id)
		for i, o := range st.order {
			if o == id {
				st.order = append(st.order[:i], st.order[i+1:]...)
				break
			}
		}
		return nil
	})
}

// Count returns the number of stored credentials.
func (st *Store) Count() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.freshLocked()
	return len(st.entries)
}

// HandleForClass returns the first credential handle with the given class,
// or nil if none exists. Order is insertion order (deterministic).
func (st *Store) HandleForClass(class string) *scheme.Handle {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.freshLocked()
	for _, id := range st.order {
		if st.entries[id].class == class {
			return &scheme.Handle{Class: class, ID: id}
		}
	}
	return nil
}

// Info is what may be said about a credential: everything but its secrets.
type Info struct {
	ID       string
	Class    string
	Kind     string // KindStatic or KindOAuth2
	Created  time.Time
	Provider string    // oauth2: the provider profile's name, or "custom"
	Scopes   []string  // oauth2: the scopes the token was granted
	Expires  time.Time // oauth2: when the access token expires; zero if it does not
	// Refreshable means an oauth2 token set has a refresh token.
	Refreshable bool
	// NeedsReauth means refreshing failed for good: every use is denied
	// until the user authorizes again (nodwyn credential login).
	NeedsReauth bool
	// TokenHost is the host of the oauth2 token endpoint refreshes go to.
	TokenHost string
}

func (e entry) info(id string) Info {
	in := Info{ID: id, Class: e.class, Kind: e.kind(), Created: e.created}
	if t := e.oauth; t != nil {
		in.Provider, in.Scopes, in.Expires = t.Provider, append([]string(nil), t.Scopes...), t.ExpiresAt
		in.Refreshable, in.NeedsReauth, in.TokenHost = t.RefreshToken != "", t.NeedsReauth, hostOf(t.TokenEndpoint)
	}
	return in
}

// Info describes the credential id.
func (st *Store) Info(id string) (Info, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.freshLocked()
	e, ok := st.entries[id]
	if !ok {
		return Info{}, errNotFound
	}
	return e.info(id), nil
}

// List describes every credential, in the order they were added.
func (st *Store) List() []Info {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.freshLocked()
	out := make([]Info, 0, len(st.order))
	for _, id := range st.order {
		out = append(out, st.entries[id].info(id))
	}
	return out
}

// Inject* are the only ways a value leaves the package: each writes the
// secret for id into an outgoing request at a named position, or into a
// starting command's environment (ADR 5).

// InjectHeader sets the request header name to prefix + value. prefix is
// the auth scheme with its trailing space ("Bearer "), or "" for a bare
// header such as x-api-key.
func (st *Store) InjectHeader(id, name, prefix string, req *http.Request) error {
	if err := CheckTransport(req); err != nil {
		return err
	}
	if err := st.checkOpenAISink(id, name, prefix, req); err != nil {
		return err
	}
	v, err := st.secret(id)
	if err != nil {
		return err
	}
	req.Header.Set(name, prefix+string(v))
	return nil
}

// InjectQuery sets the value as a URL query parameter key.
func (st *Store) InjectQuery(id, key string, req *http.Request) error {
	if err := CheckTransport(req); err != nil {
		return err
	}
	if err := st.checkOpenAISink(id, "", "", req); err != nil {
		return err
	}
	v, err := st.secret(id)
	if err != nil {
		return err
	}
	q := req.URL.Query()
	q.Set(key, string(v))
	req.URL.RawQuery = q.Encode()
	return nil
}

// InjectBody makes the value the request body.
func (st *Store) InjectBody(id string, req *http.Request) error {
	if err := CheckTransport(req); err != nil {
		return err
	}
	if err := st.checkOpenAISink(id, "", "", req); err != nil {
		return err
	}
	v, err := st.secret(id)
	if err != nil {
		return err
	}
	req.Body = io.NopCloser(bytes.NewReader(v))
	req.ContentLength = int64(len(v))
	return nil
}

// InjectEnv sets the value as the environment variable name of a command
// about to start: the container runtime that runs a contained MCP server,
// which passes it on by name only (ADR 23 W3b). It never appears in the
// command's arguments.
func (st *Store) InjectEnv(id, name string, cmd *exec.Cmd) error {
	if err := st.checkOpenAISink(id, "", "", nil); err != nil {
		return err
	}
	v, err := st.secret(id)
	if err != nil {
		return err
	}
	cmd.Env = append(cmd.Env, name+"="+string(v))
	return nil
}

// secret is what is injected for id: a static value, or an oauth2 access
// token that has not expired. An oauth2 token is made fresh by Refresh,
// which the caller runs first so the refresh is logged; one that expired
// anyway is refused, never sent stale.
func (st *Store) secret(id string) (value, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.freshLocked()
	e, ok := st.entries[id]
	if !ok {
		return nil, errNotFound
	}
	if t := e.oauth; t != nil {
		if t.NeedsReauth {
			return nil, reauthError(id, e.class)
		}
		if !t.ExpiresAt.IsZero() && !st.now().Before(t.ExpiresAt) {
			return nil, fmt.Errorf("keychain: the %s access token expired and was not refreshed", e.class)
		}
		return value(t.AccessToken), nil
	}
	return e.value, nil
}

// publishedError means the new file is visible but its directory could not
// be synced. Callers must report uncertain durability without undoing memory
// to a key that no longer opens the file.
type publishedError struct{ err error }

func (e *publishedError) Error() string {
	return "keychain: saved, but directory sync failed: " + e.err.Error()
}
func (e *publishedError) Unwrap() error { return e.err }

// save re-seals all entries and writes the file. The caller holds st.mu.
func (st *Store) save() error {
	doc := wireDoc{Version: 1, OpenAIHost: st.openAIHostID}
	for _, id := range st.order {
		e := st.entries[id]
		we := wireE{ID: id, Class: e.class, Created: e.created}
		if e.oauth != nil {
			doc.Version = 2
			t := *e.oauth
			we.Kind, we.OAuth2 = KindOAuth2, &t
		} else {
			we.Value = base64.StdEncoding.EncodeToString(e.value)
		}
		doc.Entries = append(doc.Entries, we)
	}
	if st.openAIHostID != "" {
		doc.Version = 3
	}
	pt, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	nonce := random(chacha20poly1305.NonceSizeX)
	ct := st.aead.Seal(nil, nonce, pt, magic)

	buf := make([]byte, 0, len(magic)+9+saltLen+len(nonce)+len(ct))
	buf = append(buf, magic...)
	var t [4]byte
	binary.LittleEndian.PutUint32(t[:], st.kdf.Time)
	buf = append(buf, t[:]...)
	binary.LittleEndian.PutUint32(t[:], st.kdf.Memory)
	buf = append(buf, t[:]...)
	buf = append(buf, st.kdf.Threads)
	buf = append(buf, st.salt...)
	buf = append(buf, nonce...)
	buf = append(buf, ct...)
	if len(buf) > maxFileSize {
		return errors.New("keychain: file exceeds 16 MiB")
	}
	// An exclusive sibling cannot follow a planted .tmp symlink or retain
	// its permissions. Sync both the contents and the published directory entry.
	tmp, err := os.CreateTemp(filepath.Dir(st.path), ".keychain-*")
	if err != nil {
		return fmt.Errorf("keychain: write: %w", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := tmp.Write(buf); err != nil {
		return fmt.Errorf("keychain: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("keychain: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), st.path); err != nil {
		return fmt.Errorf("keychain: write: %w", err)
	}
	st.stamp = stampOf(st.path)
	st.pending = nil
	dir, err := os.Open(filepath.Dir(st.path))
	if err != nil {
		return &publishedError{err}
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return &publishedError{err}
	}
	return nil
}

func (st *Store) decode(pt []byte) error {
	var doc wireDoc
	if err := json.Unmarshal(pt, &doc); err != nil {
		return fmt.Errorf("keychain: decode: %w", err)
	}
	if doc.Version != 1 && doc.Version != 2 && doc.Version != 3 {
		return fmt.Errorf("keychain: unsupported version %d", doc.Version)
	}
	st.openAIHostID = doc.OpenAIHost
	for _, we := range doc.Entries {
		e := entry{class: we.Class, created: we.Created}
		switch we.Kind {
		case "", KindStatic:
			v, err := base64.StdEncoding.DecodeString(we.Value)
			if err != nil {
				return fmt.Errorf("keychain: value for %s: %w", we.ID, err)
			}
			e.value = value(v)
		case KindOAuth2:
			if we.OAuth2 == nil {
				return fmt.Errorf("keychain: oauth2 credential %s has no token set", we.ID)
			}
			t := *we.OAuth2
			e.oauth = &t
		default:
			return fmt.Errorf("keychain: credential %s has unknown kind %q", we.ID, we.Kind)
		}
		st.entries[we.ID] = e
		st.order = append(st.order, we.ID)
	}
	return nil
}
