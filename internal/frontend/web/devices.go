package web

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
)

// Devices remembers the browsers paired from another machine (ADR 30): a
// name the person chose and the SHA-256 of the token its cookie carries.
// Tokens themselves are never stored, so the file cannot pair a browser.
type Devices struct {
	path string
	mu   sync.Mutex
}

// Device is one paired browser.
type Device struct {
	Name     string    `toml:"name" json:"name"`
	Hash     string    `toml:"token_sha256" json:"-"`
	Paired   time.Time `toml:"paired" json:"paired"`
	LastSeen time.Time `toml:"last_seen" json:"last_seen"`
}

type deviceFile struct {
	Device []Device `toml:"device"`
}

// DeviceName is what a device may be called: it becomes part of the actor
// recorded with every answer it gives (web:NAME).
var DeviceName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

// OpenDevices uses path (devices.toml in the config directory).
func OpenDevices(path string) *Devices { return &Devices{path: path} }

func (d *Devices) load() ([]Device, error) {
	var f deviceFile
	_, err := toml.DecodeFile(d.path, &f)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return f.Device, err
}

func (d *Devices) save(devices []Device) error {
	dir := filepath.Dir(d.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".devices-*")
	if err != nil {
		return err
	}
	_, _ = tmp.WriteString("# Browsers paired with nodwyn from other machines. Revoke with: nodwyn devices revoke NAME\n")
	werr := toml.NewEncoder(tmp).Encode(deviceFile{devices})
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr, os.Chmod(tmp.Name(), 0o600)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), d.path)
}

func digest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Pair records a device under name, replacing one of the same name, and
// returns the token its cookie carries.
func (d *Devices) Pair(name string) (string, error) {
	if !DeviceName.MatchString(name) {
		return "", fmt.Errorf("name the device with lower-case letters, digits, dots or dashes (at most 32)")
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	d.mu.Lock()
	defer d.mu.Unlock()
	devices, err := d.load()
	if err != nil {
		return "", err
	}
	devices = slices.DeleteFunc(devices, func(x Device) bool { return x.Name == name })
	now := time.Now().UTC().Truncate(time.Second)
	devices = append(devices, Device{Name: name, Hash: digest(token), Paired: now, LastSeen: now})
	return token, d.save(devices)
}

// Check returns the name of the device token belongs to. The file is read
// on every check, so a revocation applies at once: to new requests, and to
// an open console at its next request or recheck.
func (d *Devices) Check(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	want := digest(token)
	d.mu.Lock()
	defer d.mu.Unlock()
	devices, err := d.load()
	if err != nil {
		return "", false
	}
	for i, x := range devices {
		if subtle.ConstantTimeCompare([]byte(x.Hash), []byte(want)) == 1 {
			// Last seen is kept to the hour: enough to spot a stale device
			// without rewriting the file on every request.
			if now := time.Now().UTC(); now.Sub(x.LastSeen) > time.Hour {
				devices[i].LastSeen = now.Truncate(time.Second)
				_ = d.save(devices)
			}
			return x.Name, true
		}
	}
	return "", false
}

// List returns the paired devices.
func (d *Devices) List() ([]Device, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.load()
}

// Revoke forgets a device; its cookie stops working at its next request.
func (d *Devices) Revoke(name string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	devices, err := d.load()
	if err != nil {
		return err
	}
	kept := slices.DeleteFunc(slices.Clone(devices), func(x Device) bool { return x.Name == name })
	if len(kept) == len(devices) {
		return fmt.Errorf("no paired device is named %q", name)
	}
	return d.save(kept)
}
