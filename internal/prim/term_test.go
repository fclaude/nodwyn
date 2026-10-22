package prim

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	// The emulator the primitives use is linked in by the binary; a test
	// binary links it itself.
	"github.com/fclaude/brote/scheme"
	_ "github.com/fclaude/nodwyn/internal/frontend/termvt"
)

func termInterp(t *testing.T) (*scheme.Interp, *Terms) {
	t.Helper()
	ts := NewTerms()
	t.Cleanup(ts.Close)
	o := DefaultOptions(t.TempDir())
	o.Terms = ts
	o.EnvAllow = []string{"PATH"}
	in := scheme.New()
	if err := Register(in, o); err != nil {
		t.Fatal(err)
	}
	return in, ts
}

func evalErr(t *testing.T, in *scheme.Interp, src string) error {
	t.Helper()
	if _, err := in.EvalString(context.Background(), src); err != nil {
		return err
	}
	t.Fatalf("%s: accepted", src)
	return nil
}

// keyApp prints the bytes of every key it is sent as hex on row 3, in a raw
// terminal, which is what a full-screen program reads.
const keyApp = `stty raw -echo; printf '\033[2J\033[H'; printf ready; while :; do k=$(dd bs=16 count=1 2>/dev/null | od -An -tx1 | tr -s ' '); printf '\033[3;1Hkey:%s\033[K' "$k"; done`

func TestTermIsATerminal(t *testing.T) {
	in, _ := termInterp(t)
	out := evalStr(t, in, `(term-open "sh" (list "-c" "if [ -t 0 ]; then echo tty; fi; stty size; exec cat") 80 24)`)
	for _, want := range []string{"t1: running (sh -c ", " 80x24, cursor row 3 col 1", "1| tty", "2| 24 80"} {
		if !strings.Contains(out, want) {
			t.Fatalf("open: missing %q in\n%s", want, out)
		}
	}
	// The terminal echoes what is typed, and cat prints it again.
	out = evalStr(t, in, `(term-type "t1" "basil\n")`)
	if strings.Count(out, "basil") != 2 {
		t.Fatalf("type: want the echo and cat's copy in\n%s", out)
	}
	if !strings.Contains(out, "cursor row 5 col 1") {
		t.Fatalf("cursor after two lines of basil:\n%s", out)
	}
	// Several keys in one call arrive in order.
	out = evalStr(t, in, `(term-keys "t1" (list "x" "y" "Enter"))`)
	if strings.Count(out, "xy") != 2 || !strings.Contains(out, "cursor row 7 col 1") {
		t.Fatalf("keys: want xy twice in\n%s", out)
	}
}

func TestTermKeysReachTheProgramAsATerminalSendsThem(t *testing.T) {
	in, _ := termInterp(t)
	evalStr(t, in, `(term-open "sh" (list "-c" `+scmString(keyApp)+`))`)
	for _, c := range []struct{ keys, hex string }{
		{`"Enter"`, "0d"},
		{`"Escape"`, "1b"},
		{`"Tab"`, "09"},
		{`"BTab"`, "1b 5b 5a"},
		{`"BSpace"`, "7f"},
		{`"Up"`, "1b 5b 41"},
		{`"down"`, "1b 5b 42"},
		{`"PageDown"`, "1b 5b 36 7e"},
		{`"Delete"`, "1b 5b 33 7e"},
		{`"F5"`, "1b 5b 31 35 7e"},
		{`"C-a"`, "01"},
		{`"C-w"`, "17"},
		{`"M-x"`, "1b 78"},
		{`"C-M-x"`, "1b 18"},
		{`"C-Space"`, "00"},
		{`"Space"`, "20"},
		{`"é"`, "c3 a9"},
	} {
		evalStr(t, in, `(term-keys "t1" (list `+c.keys+`))`)
		out := evalStr(t, in, `(term-wait "t1" 10 "key: `+c.hex+`$")`)
		if !strings.Contains(out, "[matched after") {
			t.Errorf("keys %s: want %q\n%s", c.keys, c.hex, out)
		}
	}
}

func TestTermKeysRefusesWhatItCannotPress(t *testing.T) {
	in, _ := termInterp(t)
	evalStr(t, in, `(term-open "sh" (list "-c" `+scmString(keyApp)+`))`)
	for _, keys := range []string{`"Bogus"`, `"C-1"`, `"C-Up"`, `"X-a"`, `""`} {
		err := evalErr(t, in, `(term-keys "t1" (list `+keys+`))`)
		if !strings.Contains(err.Error(), "unknown key") || !strings.Contains(err.Error(), "PageDown") {
			t.Errorf("%s: %v", keys, err)
		}
	}
	// A list with one bad key presses none of them.
	evalErr(t, in, `(term-keys "t1" (list "x" "Bogus"))`)
	time.Sleep(300 * time.Millisecond)
	if out := evalStr(t, in, `(term-screen "t1")`); strings.Contains(out, "3| key") {
		t.Fatalf("a key was pressed:\n%s", out)
	}
	if err := evalErr(t, in, `(term-type "t9" "x")`); !strings.Contains(err.Error(), "t1") {
		t.Fatalf("unknown id should list the session's: %v", err)
	}
}

func TestTermAnswersTheProgramsQuestions(t *testing.T) {
	in, _ := termInterp(t)
	// neovim and tmux ask where the cursor is and wait for the answer.
	script := `stty raw -echo; printf 'abc\033[6n'; r=$(dd bs=16 count=1 2>/dev/null | od -An -c); printf '\nreply:%s' "$r"; exec cat`
	evalStr(t, in, `(term-open "sh" (list "-c" `+scmString(script)+`))`)
	out := evalStr(t, in, `(term-wait "t1" 10 "reply:")`)
	if !strings.Contains(out, "033   [   1   ;   4   R") {
		t.Fatalf("the cursor report was not sent back:\n%s", out)
	}
}

func TestTermScrollbackAndFullScreen(t *testing.T) {
	in, _ := termInterp(t)
	evalStr(t, in, `(term-open "sh" (list "-c" "i=1; while [ $i -le 100 ]; do echo line$i; i=$((i+1)); done; exec cat") 40 10)`)
	out := evalStr(t, in, `(term-wait "t1" 10 "line100")`)
	if !strings.Contains(out, "line100") || strings.Contains(out, "line50") {
		t.Fatalf("the live screen shows the last lines only:\n%s", out)
	}
	out = evalStr(t, in, `(term-screen "t1" 20)`)
	if !strings.Contains(out, "scrolled back 20 lines (") || !strings.Contains(out, "line81") || strings.Contains(out, "line100") {
		t.Fatalf("20 lines back:\n%s", out)
	}
	out = evalStr(t, in, `(term-screen "t1" 100000)`)
	if !strings.Contains(out, "line1\n") || !strings.Contains(out, "1| line1") || strings.Contains(out, "more above") {
		t.Fatalf("far back stops at the first line:\n%s", out)
	}
	evalStr(t, in, `(term-open "sh" (list "-c" `+scmString(`printf '\033[?1049h\033[2J\033[3;5Hfull'; exec cat`)+`))`)
	out = evalStr(t, in, `(term-wait "t2" 10 "full")`)
	if !strings.Contains(out, "full-screen") || !strings.Contains(out, "3|     full") || !strings.Contains(out, "cursor row 3 col 9") {
		t.Fatalf("alternate screen:\n%s", out)
	}
	if err := evalErr(t, in, `(term-screen "t2" 5)`); !strings.Contains(err.Error(), "full-screen") {
		t.Fatalf("scrolling a full-screen program: %v", err)
	}
}

func TestTermShowsRunsOfBlankRowsAsOneLine(t *testing.T) {
	in, _ := termInterp(t)
	evalStr(t, in, `(term-open "sh" (list "-c" `+scmString(`printf '\033[?1049h\033[2J\033[1;1Htop\033[2;1H\033[4;1Hfour\033[20;1Hbottom'; exec cat`)+`) 40 20)`)
	out := evalStr(t, in, `(term-wait "t1" 10 "bottom")`)
	for _, want := range []string{"\n 1| top\n", "\n 2|\n 3|\n", " 5-19| (blank)\n", "\n20| bottom\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("want %q in\n%s", want, out)
		}
	}
}

func TestTermWaitSaysWhyItReturned(t *testing.T) {
	in, _ := termInterp(t)
	evalStr(t, in, `(term-open "sh" (list "-c" "sleep 1; echo marker; exec cat"))`)
	start := time.Now()
	out := evalStr(t, in, `(term-wait "t1" 20 "mark.r")`)
	if !strings.Contains(out, "[matched after") || !strings.Contains(out, "marker") || time.Since(start) > 15*time.Second {
		t.Fatalf("until:\n%s", out)
	}
	if out := evalStr(t, in, `(term-wait "t1" 20 "marker")`); !strings.Contains(out, "[matched after 0.") {
		t.Fatalf("a match already on the screen returns at once:\n%s", out)
	}
	if out := evalStr(t, in, `(term-wait "t1" 1 "never shown")`); !strings.Contains(out, "[timed out after 1.") {
		t.Fatalf("timeout:\n%s", out)
	}
	if out := evalStr(t, in, `(term-wait "t1")`); !strings.Contains(out, "[quiet after") {
		t.Fatalf("quiet:\n%s", out)
	}
	for _, src := range []string{`(term-wait "t1" 61)`, `(term-wait "t1" -1)`, `(term-wait "t1" 5 "(")`, `(term-screen "t1" -1)`} {
		evalErr(t, in, src)
	}
}

func TestTermExitStaysReadableUntilClosed(t *testing.T) {
	in, _ := termInterp(t)
	evalStr(t, in, `(term-open "sh" (list "-c" "echo parting words; exit 3"))`)
	out := evalStr(t, in, `(term-wait "t1" 10)`)
	if !strings.Contains(out, "[exited after") || !strings.Contains(out, "t1: exited 3 ") || !strings.Contains(out, "parting words") {
		t.Fatalf("exit:\n%s", out)
	}
	for _, src := range []string{`(term-type "t1" "x")`, `(term-keys "t1" (list "x"))`} {
		if err := evalErr(t, in, src); !strings.Contains(err.Error(), "has exited") {
			t.Errorf("%s: %v", src, err)
		}
	}
	if out := evalStr(t, in, `(term-close "t1")`); !strings.Contains(out, "parting words") {
		t.Fatalf("close:\n%s", out)
	}
	if err := evalErr(t, in, `(term-screen "t1")`); !strings.Contains(err.Error(), "no terminal") {
		t.Fatalf("after close: %v", err)
	}
}

func TestTermLimitAndHangUp(t *testing.T) {
	in, ts := termInterp(t)
	for range termLimit {
		evalStr(t, in, `(term-open "cat" (list))`)
	}
	if err := evalErr(t, in, `(term-open "cat" (list))`); !strings.Contains(err.Error(), "already open") {
		t.Fatalf("limit: %v", err)
	}
	if out := evalStr(t, in, `(term-close "t1")`); !strings.Contains(out, "t1: exited 129 (stopped by SIGHUP)") {
		t.Fatalf("close hangs up:\n%s", out)
	}
	evalStr(t, in, `(term-open "cat" (list))`)
	ts.Close()
	for _, id := range []string{"t2", "t3", "t4"} {
		ts.mu.Lock()
		pid := ts.terms[id].cmd.Process.Pid
		ts.mu.Unlock()
		if err := syscall.Kill(-pid, 0); err == nil {
			t.Errorf("%s still runs after the session closed", id)
		}
	}
	if err := evalErr(t, in, `(term-open "cat" (list))`); !strings.Contains(err.Error(), "closing") {
		t.Fatalf("open after close: %v", err)
	}
}

func TestTermSizeIsChecked(t *testing.T) {
	in, _ := termInterp(t)
	for _, src := range []string{`(term-open "cat" (list) 10 30)`, `(term-open "cat" (list) 100 4)`, `(term-open "cat" (list) 300 30)`, `(term-open "no-such-program-here" (list))`} {
		evalErr(t, in, src)
	}
}

// A model that runs tmux must not reach the person's own tmux server, and
// the one it starts must end with the session (a tmux server leaves its
// terminal and is not hung up on).
func TestTermTmuxIsPrivateAndEndsWithTheSession(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	in, ts := termInterp(t)
	out := evalStr(t, in, `(term-open "tmux" (list "new-session" "-s" "inner") 80 24)`)
	if !strings.Contains(out, "[inner]") {
		out = evalStr(t, in, `(term-wait "t1" 10 "\\[inner\\]")`)
	}
	if !strings.Contains(out, "[inner]") {
		t.Fatalf("tmux status line:\n%s", out)
	}
	socket := filepath.Join(ts.tmuxDir, fmt.Sprintf("tmux-%d", os.Getuid()), "default")
	if _, err := os.Stat(socket); err != nil {
		t.Fatalf("tmux did not use the session's directory: %v", err)
	}
	// Detach, hang up: the server stays up, as it would for a person.
	evalStr(t, in, `(term-keys "t1" (list "C-b" "d"))`)
	evalStr(t, in, `(term-wait "t1" 10)`)
	evalStr(t, in, `(term-close "t1")`)
	list := exec.Command("tmux", "-S", socket, "list-sessions")
	if b, err := list.CombinedOutput(); err != nil || !strings.Contains(string(b), "inner") {
		t.Fatalf("the server should outlive its terminal: %v %s", err, b)
	}
	ts.Close()
	if _, err := os.Stat(ts.tmuxDir); !os.IsNotExist(err) {
		t.Fatalf("the tmux directory is left: %v", err)
	}
	if err := exec.Command("tmux", "-S", socket, "list-sessions").Run(); err == nil {
		t.Fatal("the tmux server outlived the session")
	}
}

func TestParseKey(t *testing.T) {
	for _, ok := range []string{"a", "A", "5", ":", "-", "é", "Enter", "ENTER", "esc", "F12", "C-x", "c-X", "C-[", "M-x", "M-Up", "C-M-x", "BTab", "space", "C-Space", "M--"} {
		if _, err := parseKey(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "ab", "C-", "C-1", "C-Up", "S-a", "F13", "Bogus", "M-"} {
		if _, err := parseKey(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// scmString quotes s as a Scheme string literal.
func scmString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
