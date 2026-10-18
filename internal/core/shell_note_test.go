package core

import (
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// shell-exec's result carries a note when a command exited 141 (a downstream
// head stopped reading) or read a file and printed nothing, and the shell
// habit gave the model the status and the streams alone, so no model ever
// read either: the log and the clients showed them. A lean session reads the
// note on a line of its own after the output; a full session's text is what it
// was (ADR 32, amended 2026-10-17).
func TestAShellNoteReachesTheModelOnlyInTheLeanProfile(t *testing.T) {
	for _, profile := range []string{config.ProfileLean, config.ProfileFull} {
		useProfile(t, profile)
		r := profileRig(t, unattendedOptions, "",
			shell("cut", "echo some; echo more >&2; exit 141"),
			shellArgs("empty", "sed", "-n", "10,20p", "main.go"),
			shell("plain", "echo fine"),
			fake.Turn{Content: "done"}, fake.Turn{Content: "done"}, fake.Turn{Content: "done"}, fake.Turn{Content: "done"},
		)
		_ = r.turn("look at it")
		reads := map[string]string{"cut": lastMessage(r, 1), "empty": lastMessage(r, 2), "plain": lastMessage(r, 3)}
		for id, want := range map[string]string{
			"cut":   "exit 141\nsome\n\n[stderr]\nmore\n[note] exit 141: the output was cut because a command later in the pipe (such as head) stopped reading; there may be more",
			"empty": "exit 0\n[note] exit 0 with no output: a command that reads a file printed nothing, so its range may lie past the end of the file (wc -l FILE counts the lines) or the file is empty",
		} {
			got := reads[id]
			if profile == config.ProfileLean {
				// A hint other habits add to a read goes after the note.
				if !strings.HasPrefix(got, want) {
					t.Errorf("lean, %s: the model read\n%q\nwant it to start\n%q", id, got, want)
				}
			} else if strings.Contains(got, "[note]") || !strings.HasPrefix(want, strings.SplitN(got, "\n", 2)[0]) {
				t.Errorf("full, %s: the model read %q", id, got)
			}
		}
		if got := reads["plain"]; got != "exit 0\nfine\n" {
			t.Errorf("%s: a result with no note reads %q", profile, got)
		}
	}
}

// A long output reaches a lean session's model as a view with a marker, and
// the marker's own command reads the saved lines without a question; a full
// session's model reads the output whole, as it did.
func TestALongOutputIsAViewOnlyInTheLeanProfile(t *testing.T) {
	const gen = `awk 'BEGIN { for (i = 1; i <= 330; i++) printf "line %03d of the output, padded ...............\n", i }'`
	for _, profile := range []string{config.ProfileLean, config.ProfileFull} {
		useProfile(t, profile)
		r := profileRig(t, unattendedOptions, "",
			shell("long", gen),
			shell("range", `sed -n '21,22p' $TMPDIR/out/1.stdout`),
			shellArgs("args", "grep", "-n", "line 300", "$TMPDIR/out/1.stdout"),
			fake.Turn{Content: "done"}, fake.Turn{Content: "done"}, fake.Turn{Content: "done"}, fake.Turn{Content: "done"},
		)
		_ = r.turn("run it")
		long, ranged, grepped := lastMessage(r, 1), lastMessage(r, 2), lastMessage(r, 3)
		if profile == config.ProfileFull {
			// What it reads is the shell habit's own cut (shell-clip), not a view.
			if !strings.Contains(long, "characters omitted") || strings.Contains(long, "not shown") || strings.Contains(long, "$TMPDIR/out") {
				t.Errorf("full: the model read a view: %.200q", long)
			}
			if strings.Contains(ranged, "line 021") {
				t.Errorf("full: there is a saved output to read: %q", ranged)
			}
			continue
		}
		const marker = "[... lines 21-280 of 330 not shown; full output is saved at $TMPDIR/out/1.stdout: sed -n '21,120p' $TMPDIR/out/1.stdout shows a range, grep -n PATTERN $TMPDIR/out/1.stdout searches it ...]\n"
		if !strings.HasPrefix(long, "exit 0\nline 001 of the output") || !strings.Contains(long, "line 020 of the output, padded ...............\n"+marker+"line 281 of the output") || !strings.HasSuffix(long, "line 330 of the output, padded ...............\n") {
			t.Errorf("lean: the model read\n%s", long)
		}
		if ranged != "exit 0\nline 021 of the output, padded ...............\nline 022 of the output, padded ...............\n" {
			t.Errorf("lean: the marker's sed read %q", ranged)
		}
		if grepped != "exit 0\n300:line 300 of the output, padded ...............\n" {
			t.Errorf("lean: the marker's grep read %q", grepped)
		}
	}
}
