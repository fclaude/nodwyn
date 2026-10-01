package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/term"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/keychain"
)

// keychain creates the encrypted credential store. It exists only as
// "keychain init"; its credentials are managed with "credential"
// (credential.go), and static keys are registered from inside a session.
func (c *cli) keychain() error {
	if c.fs.Arg(1) != "init" {
		return fmt.Errorf("keychain: expected 'init'")
	}
	if _, err := os.Stat(c.paths.Keychain); err == nil {
		return fmt.Errorf("keychain already exists at %s", c.paths.Keychain)
	}
	pass, err := readPassphrase("new keychain passphrase: ")
	if err != nil {
		return err
	}
	again, err := readPassphrase("again: ")
	if err != nil {
		return err
	}
	if string(pass) != string(again) {
		return fmt.Errorf("passphrases do not match")
	}
	if len(pass) == 0 {
		return fmt.Errorf("empty passphrase")
	}
	if err := os.MkdirAll(filepath.Dir(c.paths.Keychain), 0o700); err != nil {
		return err
	}
	if _, err := keychain.Create(c.paths.Keychain, keychain.DefaultKDF, pass); err != nil {
		return err
	}
	fmt.Println("keychain created at", c.paths.Keychain)
	return nil
}

// readPassphrase reads a masked line from the terminal. Passphrases never
// come from the environment or from arguments (plan §3.3).
func readPassphrase(prompt string) ([]byte, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, fmt.Errorf("a passphrase needs a terminal")
	}
	return readPassphraseFrom(os.Stdin, os.Stderr, prompt)
}

func readPassphraseFrom(tty *os.File, out io.Writer, prompt string) ([]byte, error) {
	fmt.Fprint(out, prompt)
	secret, err := term.ReadPassword(int(tty.Fd()))
	fmt.Fprintln(out)
	return secret, err
}

// openKeychain opens the store if one exists and stdin is a terminal, or,
// with controllingTTY, when the process has a controlling terminal
// (/dev/tty) — as ssh and sudo ask — so a session whose input is a script
// can still be unlocked by the person who started it.
// Without either the session simply has no credential primitives, which is
// the bootstrap posture, so a missing keychain is not an error.
func openKeychain(paths config.Paths, controllingTTY bool) (*keychain.Store, binding.Table, error) {
	if _, err := os.Stat(paths.Keychain); err != nil {
		return nil, binding.Table{}, nil
	}
	tty, out := os.Stdin, io.Writer(os.Stderr)
	if !term.IsTerminal(int(tty.Fd())) {
		var err error
		tty = nil
		if controllingTTY {
			if tty, err = os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil && !term.IsTerminal(int(tty.Fd())) {
				tty.Close()
				tty = nil
			}
		}
		if tty == nil {
			fmt.Fprintln(os.Stderr, "keychain: not a terminal; starting without credentials (use --no-keychain to silence)")
			return nil, binding.Table{}, nil
		}
		defer tty.Close()
		out = tty // stderr may be a log; the prompt is for the person at the terminal
	}
	for attempt := 0; attempt < 3; attempt++ {
		pass, err := readPassphraseFrom(tty, out, "keychain passphrase: ")
		if err != nil {
			return nil, binding.Table{}, err
		}
		store, err := keychain.Open(paths.Keychain, pass)
		if err != nil {
			fmt.Fprintln(out, "keychain:", err)
			continue
		}
		table, err := binding.Load(paths.Bindings)
		if err != nil {
			return nil, binding.Table{}, err
		}
		fmt.Fprintf(os.Stderr, "keychain: %d credential(s), %d binding row(s)\n", store.Count(), len(table.Bind))
		return store, table, nil
	}
	return nil, binding.Table{}, fmt.Errorf("keychain: could not open")
}
