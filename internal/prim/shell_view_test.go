package prim

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/fclaude/nodwyn/internal/guard"
	nlog "github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/scheme"
	"github.com/fclaude/nodwyn/internal/window"
)

// A long output is shown as a view with a file behind it (shellview.go): the
// tests here run commands under a session whose scratch directory is its own.

// shellOut is a shell-exec result.
type shellOut struct {
	Status any    `json:"status"`
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Note   string `json:"note"`
}

// viewRig is a session's shell-exec, as the loop calls it: a model's call
// through the guard's checkers, with a scratch directory of its own.
type viewRig struct {
	h       *harness
	scratch string
	opts    Options
}

func newViewRig(t *testing.T, mod ...func(*Options)) *viewRig {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	scratch, _ := filepath.EvalSymlinks(t.TempDir())
	h := &harness{root: root, logBuf: &bytes.Buffer{}, answer: true}
	h.in = scheme.New()
	o := DefaultOptions(root)
	o.Scratch = scratch
	for _, m := range mod {
		m(&o)
	}
	if err := Register(h.in, o); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	confirm := guard.ConfirmFunc(func(_ context.Context, p guard.Prompt) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		h.prompts = append(h.prompts, p)
		return true, nil
	})
	h.in.SetGate(guard.New(confirm, recorder{nlog.New(h.logBuf, "test")}, Checkers(o, nil, nil)...))
	// $TMPDIR in a command's arguments is the scratch directory, as the core
	// has it (expandScratch).
	h.in.SetExpand(func(s string) string {
		if s == "$TMPDIR" || strings.HasPrefix(s, "$TMPDIR/") {
			return scratch + strings.TrimPrefix(s, "$TMPDIR")
		}
		return s
	})
	return &viewRig{h: h, scratch: scratch, opts: o}
}

// run runs a bash -c script as a model's call.
func (r *viewRig) run(t *testing.T, script string) shellOut {
	t.Helper()
	return r.runIn(t, scheme.WithOrigin(context.Background(), scheme.OriginModel), "bash", "-c", script)
}

func (r *viewRig) runIn(t *testing.T, ctx context.Context, cmd string, args ...string) shellOut {
	t.Helper()
	p, _ := r.h.in.Lookup("shell-exec")
	list := make([]scheme.Value, len(args))
	for i, a := range args {
		list[i] = scheme.Str(a)
	}
	v, err := r.h.in.Apply(ctx, scheme.Value{Kind: scheme.KPrim, Prim: p}, []scheme.Value{scheme.Str(cmd), scheme.List(list...)})
	if err != nil {
		t.Fatalf("%s %v: %v", cmd, args, err)
	}
	raw, err := scheme.ToJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	var out shellOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// saved lists the files of the session's saved outputs.
func (r *viewRig) saved(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(r.scratch, "out"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func (r *viewRig) file(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.scratch, "out", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// lineN is line i of a fixture output: 46 bytes with its line end.
func lineN(i int) string { return fmt.Sprintf("line %03d of the output, padded ...............", i) }

// emit is a bash script that prints lines 1..n of the fixture.
func emit(n int) string {
	return fmt.Sprintf(`awk 'BEGIN { for (i = 1; i <= %d; i++) printf "line %%03d of the output, padded ...............\n", i }'`, n)
}

func joinLines(from, to int) string {
	var b strings.Builder
	for i := from; i <= to; i++ {
		b.WriteString(lineN(i) + "\n")
	}
	return b.String()
}

// A stream over the thresholds is shown as its first 20 lines, a marker where
// the others are, and its last 50, and the whole of it is on disk.
func TestShellExecShowsALongStreamAsAView(t *testing.T) {
	r := newViewRig(t)
	got := r.run(t, emit(330))
	marker := "[... lines 21-280 of 330 not shown; full output is saved at $TMPDIR/out/1.stdout: sed -n '21,120p' $TMPDIR/out/1.stdout shows a range, grep -n PATTERN $TMPDIR/out/1.stdout searches it ...]\n"
	if want := joinLines(1, 20) + marker + joinLines(281, 330); got.Stdout != want {
		t.Fatalf("view:\n%s\nwant:\n%s", got.Stdout, want)
	}
	if got.Stderr != "" || got.Status != float64(0) {
		t.Fatalf("status %v stderr %q", got.Status, got.Stderr)
	}
	if len(got.Stdout) > 6<<10 {
		t.Fatalf("the view is %d bytes", len(got.Stdout))
	}
	if saved := r.file(t, "1.stdout"); saved != joinLines(1, 330) {
		t.Fatalf("the saved file is %d bytes, want the whole output (%d)", len(saved), len(joinLines(1, 330)))
	}
	if files := r.saved(t); len(files) != 1 {
		t.Fatalf("files: %v (a stream that is not long has none)", files)
	}
}

// A gap of fewer than 100 lines is the range suggested, no more.
func TestShellExecMarkerRanges(t *testing.T) {
	r := newViewRig(t)
	// 160 lines: lines 21-110 are not shown.
	got := r.run(t, emit(160))
	if !strings.Contains(got.Stdout, "[... lines 21-110 of 160 not shown; full output is saved at $TMPDIR/out/1.stdout: sed -n '21,110p' $TMPDIR/out/1.stdout shows a range") {
		t.Fatalf("marker:\n%s", got.Stdout)
	}
}

// One long line is cut at 300 characters; lines that make a view too big
// are fewer, never the marker or the end.
func TestShellExecViewIsBoundedAndCutsLongLines(t *testing.T) {
	r := newViewRig(t)
	got := r.run(t, `awk 'BEGIN { for (i = 1; i <= 400; i++) { s = ""; for (k = 0; k < 30; k++) s = s "0123456789"; printf "%03d %s end\n", i, s } }'`)
	if len(got.Stdout) > 6<<10 {
		t.Fatalf("the view is %d bytes", len(got.Stdout))
	}
	lines := strings.Split(strings.TrimSuffix(got.Stdout, "\n"), "\n")
	if !strings.HasPrefix(lines[0], "001 0123456789") || !strings.HasSuffix(lines[0], "…") {
		t.Fatalf("first line: %q", lines[0])
	}
	if n := len([]rune(lines[0])); n != 301 {
		t.Fatalf("a line is cut at 300 characters and an ellipsis: %d", n)
	}
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "400 ") || !strings.HasSuffix(last, "…") {
		t.Fatalf("last line: %q", last)
	}
	marker := ""
	for _, l := range lines {
		if strings.HasPrefix(l, "[... lines ") {
			marker = l
		}
	}
	if !regexp.MustCompile(`^\[\.\.\. lines \d+-\d+ of 400 not shown; full output is saved at \$TMPDIR/out/1\.stdout: sed -n`).MatchString(marker) {
		t.Fatalf("marker: %q in\n%s", marker, got.Stdout)
	}
	// Fewer than 20 + 50 lines to fit 6 KiB of 300-character lines.
	if len(lines) >= 71 || len(lines) < 5 {
		t.Fatalf("%d lines", len(lines))
	}
	if !strings.Contains(r.file(t, "1.stdout"), "001 0123456789012345678901234567890123456789") || strings.Count(r.file(t, "1.stdout"), "\n") != 400 {
		t.Fatal("the file does not hold the lines whole")
	}
}

// A few lines that are long are all shown, cut, with a footer that says
// where the whole is.
func TestShellExecViewOfFewLongLines(t *testing.T) {
	r := newViewRig(t)
	got := r.run(t, `awk 'BEGIN { for (i = 1; i <= 4; i++) { s = ""; for (k = 0; k < 300; k++) s = s "0123456789"; printf "%d %s\n", i, s } }'`)
	if !strings.Contains(got.Stdout, "[... lines are cut at 300 characters; full output is saved at $TMPDIR/out/1.stdout") || strings.Count(got.Stdout, "…") != 4 {
		t.Fatalf("view:\n%s", got.Stdout)
	}
	if strings.Contains(got.Stdout, "not shown") {
		t.Fatalf("nothing is missing but the ends of lines:\n%s", got.Stdout)
	}
}

// The thresholds are 8 KiB and 150 lines: a stream of exactly that is shown
// as it is, one byte or line more is a view. A stream that stays under them
// leaves no file.
func TestShellExecThresholds(t *testing.T) {
	line := func(n int) string { return strings.Repeat("x", n-1) + "\n" }
	cases := []struct {
		name string
		data string
		view bool
	}{
		{"exactly 8 KiB", strings.Repeat(line(64), 128), false},
		{"8 KiB and a byte", strings.Repeat(line(64), 127) + line(65), true},
		{"exactly 150 lines", strings.Repeat("x\n", 150), false},
		{"151 lines", strings.Repeat("x\n", 151), true},
		{"150 lines and a last line without an end", strings.Repeat("x\n", 150) + "y", true},
		{"nothing", "", false},
	}
	for _, c := range cases {
		r := newViewRig(t)
		if err := os.WriteFile(filepath.Join(r.h.root, "data.txt"), []byte(c.data), 0o644); err != nil {
			t.Fatal(err)
		}
		got := r.run(t, "awk 1 data.txt")
		if shown := strings.Contains(got.Stdout, "[... "); shown != c.view {
			t.Errorf("%s: view %v, want %v", c.name, shown, c.view)
		}
		if !c.view && got.Stdout != c.data {
			t.Errorf("%s: the output changed: %d bytes for %d", c.name, len(got.Stdout), len(c.data))
		}
		if files := r.saved(t); (len(files) == 1) != c.view || len(files) > 1 {
			t.Errorf("%s: files %v", c.name, files)
		}
	}
	// By bytes alone: 70 lines of 125 bytes are over 8 KiB, and a view of them
	// would be over 6 KiB with 20 + 50 lines, so it shows fewer.
	r := newViewRig(t)
	os.WriteFile(filepath.Join(r.h.root, "data.txt"), []byte(strings.Repeat(line(125), 70)), 0o644)
	if got := r.run(t, "awk 1 data.txt"); !strings.Contains(got.Stdout, " of 70 not shown;") || len(got.Stdout) > 6<<10 {
		t.Errorf("70 lines over 8 KiB: %d bytes\n%.400s", len(got.Stdout), got.Stdout)
	}
}

// A command that reads a file is not shown as a view: it prints the range it
// was asked for, up to the old cap.
func TestShellExecReadLikeCommandsAreNotCut(t *testing.T) {
	r := newViewRig(t)
	data := joinLines(1, 400)
	if err := os.WriteFile(filepath.Join(r.h.root, "data.txt"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{
		"cat data.txt",
		"sed -n '1,400p' data.txt",
		"head -n 400 data.txt",
		"tail -n 400 data.txt",
		"awk 1 data.txt | tail -n 400",
		"awk 1 data.txt | head -n 400",
	} {
		got := r.run(t, script)
		if got.Stdout != data {
			t.Errorf("%s: %d bytes of %d, starts %.60q", script, len(got.Stdout), len(data), got.Stdout)
		}
	}
	got := r.run(t, "nl data.txt")
	if strings.Contains(got.Stdout, "not shown") || !strings.Contains(got.Stdout, "400\tline 400 of the output") {
		t.Errorf("nl:\n%.200s", got.Stdout)
	}
	if files := r.saved(t); len(files) != 0 {
		t.Errorf("a read left files: %v", files)
	}
	// The same lines from a program that is not a read are a view.
	if got := r.run(t, "awk 1 data.txt"); !strings.Contains(got.Stdout, "lines 21-350 of 400 not shown") {
		t.Errorf("awk:\n%.300s", got.Stdout)
	}
	// Past the old cap a read keeps its first and last halves, as it did.
	big := strings.Repeat(lineN(1)+"\n", 7000) // 322 KB
	os.WriteFile(filepath.Join(r.h.root, "big.txt"), []byte(big), 0o644)
	got = r.run(t, "cat big.txt")
	if !strings.Contains(got.Stdout, "omitted: only the first") || len(got.Stdout) > 256<<10+200 {
		t.Errorf("cat of 322 KB: %d bytes", len(got.Stdout))
	}
}

// Each stream is judged alone: errors alone are a view in N.stderr, and
// when both are long they share the call's number and the budget.
func TestShellExecStreamsAreViewedApart(t *testing.T) {
	r := newViewRig(t)
	got := r.run(t, `awk 'BEGIN { for (i = 1; i <= 300; i++) printf "error %03d ..................................\n", i > "/dev/stderr" }'; echo done`)
	if got.Stdout != "done\n" {
		t.Fatalf("stdout %q", got.Stdout)
	}
	if !strings.HasPrefix(got.Stderr, "error 001 ") || !strings.Contains(got.Stderr, "[... lines 21-250 of 300 not shown; full output is saved at $TMPDIR/out/1.stderr: sed -n '21,120p' $TMPDIR/out/1.stderr shows a range, grep -n PATTERN $TMPDIR/out/1.stderr searches it ...]\n") || !strings.HasSuffix(got.Stderr, "error 300 ..................................\n") {
		t.Fatalf("stderr:\n%s", got.Stderr)
	}
	if files := r.saved(t); strings.Join(files, " ") != "1.stderr" {
		t.Fatalf("files: %v", files)
	}

	r = newViewRig(t)
	got = r.run(t, `awk 'BEGIN { for (i = 1; i <= 300; i++) { printf "out %03d ..................................\n", i; printf "err %03d ..................................\n", i > "/dev/stderr" } }'`)
	if !strings.Contains(got.Stdout, "full output is saved at $TMPDIR/out/1.stdout") || !strings.Contains(got.Stderr, "full output is saved at $TMPDIR/out/1.stderr") {
		t.Fatalf("both long:\n%s\n%s", got.Stdout, got.Stderr)
	}
	if n := len(got.Stdout) + len(got.Stderr); n > 6<<10 {
		t.Fatalf("two views take %d bytes", n)
	}
	if !strings.HasSuffix(got.Stdout, "out 300 ..................................\n") || !strings.HasSuffix(got.Stderr, "err 300 ..................................\n") {
		t.Fatalf("the ends:\n%s\n%s", got.Stdout, got.Stderr)
	}
	if files := r.saved(t); strings.Join(files, " ") != "1.stderr 1.stdout" {
		t.Fatalf("files: %v", files)
	}
}

// The files are numbered 1, 2, 3 ... in the session, and a call that needs no
// file takes no number.
func TestShellExecSavedOutputsAreNumbered(t *testing.T) {
	r := newViewRig(t)
	for i, want := range []string{"1", "short", "2", "3"} {
		script := emit(200)
		if want == "short" {
			script = emit(5)
		}
		got := r.run(t, script)
		if marker := "$TMPDIR/out/" + want + ".stdout"; want != "short" && !strings.Contains(got.Stdout, marker) {
			t.Fatalf("call %d: want %s in\n%s", i+1, marker, got.Stdout)
		}
		if want == "short" && strings.Contains(got.Stdout, "[...") {
			t.Fatalf("call %d: a short output has a marker", i+1)
		}
	}
	if files := r.saved(t); strings.Join(files, " ") != "1.stdout 2.stdout 3.stdout" {
		t.Fatalf("files: %v", files)
	}
}

// Calls that run together, in one session or in sessions that share a
// scratch directory, get numbers of their own, and the file of a number is
// the output of the call that was told it.
func TestShellExecSavedOutputsAreNumberedUnderConcurrency(t *testing.T) {
	scratch, _ := filepath.EvalSymlinks(t.TempDir())
	root, _ := filepath.EvalSymlinks(t.TempDir())
	ctx := scheme.WithOrigin(context.Background(), scheme.OriginModel)
	var rigs []*scheme.Interp
	for i := 0; i < 3; i++ {
		o := DefaultOptions(root)
		o.Scratch = scratch
		in := scheme.New()
		if err := Register(in, o); err != nil {
			t.Fatal(err)
		}
		rigs = append(rigs, in)
	}
	re := regexp.MustCompile(`\$TMPDIR/out/(\d+)\.stdout`)
	var mu sync.Mutex
	owner := map[string]string{} // number -> the tag its call printed
	var wg sync.WaitGroup
	for g := 0; g < 12; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tag := fmt.Sprintf("call-%02d", g)
			v, err := rigs[g%len(rigs)].EvalString(ctx, fmt.Sprintf(`(shell-exec "bash" (list "-c" "awk 'BEGIN { for (i = 1; i <= 200; i++) print \"%s\", i }'"))`, tag))
			if err != nil {
				t.Error(err)
				return
			}
			out := scheme.Write(v)
			m := re.FindStringSubmatch(out)
			if m == nil || !strings.Contains(out, tag+" 200") {
				t.Errorf("%s: %.300s", tag, out)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if prev, dup := owner[m[1]]; dup {
				t.Errorf("number %s was given to %s and %s", m[1], prev, tag)
			}
			owner[m[1]] = tag
		}()
	}
	wg.Wait()
	for n, tag := range owner {
		b, err := os.ReadFile(filepath.Join(scratch, "out", n+".stdout"))
		if err != nil || !strings.HasPrefix(string(b), tag+" 1\n") || !strings.HasSuffix(string(b), tag+" 200\n") {
			t.Errorf("%s.stdout is not the output of %s: %v %.40q", n, tag, err, b)
		}
	}
	var numbers []int
	for n := range owner {
		var k int
		fmt.Sscan(n, &k)
		numbers = append(numbers, k)
	}
	sort.Ints(numbers)
	for i, k := range numbers {
		if k != i+1 {
			t.Fatalf("numbers %v are not 1..%d", numbers, len(numbers))
		}
	}
	if len(numbers) != 12 {
		t.Fatalf("%d numbers", len(numbers))
	}
}

// The saved file stops at 8 MiB and the marker says so; the view still ends
// with the end of the stream.
func TestShellExecSavedOutputStopsAtEightMiB(t *testing.T) {
	r := newViewRig(t)
	got := r.run(t, `awk 'BEGIN { for (i = 1; i <= 1500000; i++) print i }'`)
	st, err := os.Stat(filepath.Join(r.scratch, "out", "1.stdout"))
	if err != nil || st.Size() != 8<<20 {
		t.Fatalf("the file: %v %v", st, err)
	}
	held := strings.Count(r.file(t, "1.stdout"), "\n")
	if !strings.Contains(got.Stdout, "of 1500000 not shown") || !strings.Contains(got.Stdout, fmt.Sprintf("(the file holds the first 8 MiB of the output, up to line %d)", held)) {
		t.Fatalf("marker:\n%s", got.Stdout)
	}
	if !strings.HasSuffix(got.Stdout, "1499999\n1500000\n") {
		t.Fatalf("the end of the stream:\n%s", got.Stdout[len(got.Stdout)-100:])
	}
	if !strings.Contains(got.Stdout, "sed -n '21,120p' $TMPDIR/out/1.stdout") {
		t.Fatalf("range:\n%s", got.Stdout)
	}
}

// Without a scratch directory there is nowhere to keep the lines, and the
// stream is shown as it always was: whole up to the cap.
func TestShellExecWithoutScratchShowsTheStreamAsItWas(t *testing.T) {
	r := newViewRig(t, func(o *Options) { o.Scratch = "" })
	got := r.run(t, emit(330))
	if got.Stdout != joinLines(1, 330) {
		t.Fatalf("%d bytes, starts %.80q", len(got.Stdout), got.Stdout)
	}
	// And with a scratch directory, the same call is a view: the control.
	r = newViewRig(t)
	if got := r.run(t, emit(330)); !strings.Contains(got.Stdout, "not shown") {
		t.Fatal("with a scratch directory the call was not a view")
	}
}

// Only the model's own call is shown a view: a program of the scheme tool,
// a strategy and the person's own call compute with the output.
func TestShellExecViewIsForTheModelsOwnCall(t *testing.T) {
	r := newViewRig(t)
	if got := r.run(t, emit(330)); !strings.Contains(got.Stdout, "not shown") {
		t.Fatal("the model's call was not a view")
	}
	for name, origin := range map[string]scheme.Origin{"harness": scheme.OriginHarness, "user": scheme.OriginUser} {
		got := r.runIn(t, scheme.WithOrigin(context.Background(), origin), "bash", "-c", emit(330))
		if got.Stdout != joinLines(1, 330) {
			t.Errorf("a %s call was shown a view", name)
		}
	}
	// A scheme program counts the lines it was given.
	ctx := scheme.WithOrigin(context.Background(), scheme.OriginModel)
	v, err := runSchemeTool(ctx, r.h.in, `(length (string-split (cdr (assoc "stdout" (shell-exec "bash" (list "-c" `+scheme.Write(scheme.Str(emit(330)))+`)))) "\n"))`)
	if err != nil || scheme.Write(v) != "331" {
		t.Fatalf("a scheme program was given %s lines: %v", scheme.Write(v), err)
	}
	if files := r.saved(t); strings.Join(files, " ") != "1.stdout" {
		t.Fatalf("files: %v", files)
	}
}

// What the marker tells the model to run is allowed: a range of the saved
// file with sed -n and a search with grep -n, through the guard's checkers,
// without a question about a path outside the repository, whether the shell
// expands $TMPDIR or the arguments are given without one.
func TestShellExecSavedOutputCanBeReadThroughTheGuard(t *testing.T) {
	r := newViewRig(t)
	r.run(t, emit(330))
	asked := len(r.h.prompts)
	for script, want := range map[string]string{
		`sed -n '21,120p' $TMPDIR/out/1.stdout`:           joinLines(21, 120),
		`grep -n 'line 150' $TMPDIR/out/1.stdout`:         "150:" + lineN(150) + "\n",
		`sed -n '21,120p' ${TMPDIR}/out/1.stdout | wc -l`: "     100\n",
	} {
		if got := r.run(t, script); got.Stdout != want {
			t.Errorf("%s printed %.80q", script, got.Stdout)
		}
	}
	got := r.runIn(t, scheme.WithOrigin(context.Background(), scheme.OriginModel), "sed", "-n", "21,22p", "$TMPDIR/out/1.stdout")
	if got.Stdout != joinLines(21, 22) {
		t.Errorf("sed with the path as an argument: %q", got.Stdout)
	}
	got = r.runIn(t, scheme.WithOrigin(context.Background(), scheme.OriginModel), "grep", "-n", "line 330", "$TMPDIR/out/1.stdout")
	if got.Stdout != "330:"+lineN(330)+"\n" {
		t.Errorf("grep with the path as an argument: %q", got.Stdout)
	}
	for _, p := range r.h.prompts[asked:] {
		for _, reason := range p.Reasons {
			if strings.Contains(reason, "outside the repository") || strings.Contains(reason, "protected") {
				t.Errorf("the guard asked about reading the saved file: %v", p)
			}
		}
	}
	// The trigger alone allows them.
	trigger := guard.DefaultArgvTrigger(r.h.root)
	trigger.Scratch = r.scratch
	p, _ := r.h.in.Lookup("shell-exec")
	for _, line := range [][]string{{"bash", "-c", `sed -n '21,120p' $TMPDIR/out/7.stdout`}, {"bash", "-c", `grep -n PATTERN $TMPDIR/out/7.stderr`}, {"sed", "-n", "21,120p", scratchPath(r, "out/7.stdout")}} {
		args := []scheme.Value{scheme.Str(line[0]), scheme.List(scheme.Str(line[1]), scheme.Str(line[2]))}
		if len(line) > 3 {
			args = []scheme.Value{scheme.Str(line[0]), scheme.List(scheme.Str(line[1]), scheme.Str(line[2]), scheme.Str(line[3]))}
		}
		if d := trigger.Check(context.Background(), p, args); d.Verdict != guard.Allow {
			t.Errorf("%v: %+v", line, d)
		}
	}
	// A file outside both is still asked about.
	args := []scheme.Value{scheme.Str("sed"), scheme.List(scheme.Str("-n"), scheme.Str("1p"), scheme.Str("/etc/hosts"))}
	if d := trigger.Check(context.Background(), p, args); d.Verdict != guard.Confirm {
		t.Errorf("/etc/hosts: %+v", d)
	}
}

func scratchPath(r *viewRig, rel string) string { return filepath.Join(r.scratch, rel) }

// A Django-style run: the parallel runner prints the verdict and then only
// "Destroying test database" lines, so a model that piped the run into tail
// saw no verdict in 362 of 1,208 runs. As a view, the last 50 lines hold it.
func TestShellExecViewOfADjangoStyleRunShowsTheVerdict(t *testing.T) {
	r := newViewRig(t)
	script := `echo "Testing against Django installed in '/repo/django' with up to 8 processes"
echo "Found 12 test(s)."
for i in $(seq 1 200); do echo "test_case_$i (app.tests.CaseTests.test_case_$i) ... ok" >&2; done
echo "======================================================================" >&2
echo "FAIL: test_serialize (app.tests.XMLTests.test_serialize)" >&2
for i in $(seq 1 74); do echo "  File \"/repo/app/tests.py\", line $i, in test_serialize" >&2; done
echo "Ran 12 tests in 3.214s" >&2
echo "FAILED (failures=2)" >&2
for i in $(seq 1 20); do echo "Destroying test database for alias 'default' ('file:memorydb_default?mode=memory&cache=shared')..."; done
`
	if err := os.WriteFile(filepath.Join(r.h.root, "fake_runtests.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// 2>&1: both streams in one.
	got := r.run(t, "bash fake_runtests.sh 2>&1")
	if n := strings.Count(got.Stdout, "\n"); n < 60 || n > 75 {
		t.Fatalf("%d lines in the view", n)
	}
	for _, want := range []string{"Ran 12 tests in 3.214s\n", "FAILED (failures=2)\n", "Testing against Django installed", "[... lines 21-"} {
		if !strings.Contains(got.Stdout, want) {
			t.Errorf("the view lacks %q:\n%s", want, got.Stdout)
		}
	}
	if strings.Count(got.Stdout, "Destroying test database") != 20 {
		t.Errorf("the 20 last lines were not all shown")
	}
	if full := r.file(t, "1.stdout"); strings.Count(full, "\n") != 300 || strings.Count(full, " ... ok\n") != 200 {
		t.Errorf("the saved file holds %d lines", strings.Count(full, "\n"))
	}
	// The run the model would have piped: a tail of 15 lines hides the verdict.
	piped := r.run(t, "bash fake_runtests.sh 2>&1 | tail -n 15")
	if strings.Contains(piped.Stdout, "FAILED (failures=2)") {
		t.Fatal("the fixture's verdict is within the last 15 lines")
	}
}

// showsInWindow is what the window shows the model of a tool result: Build
// with the default cap on results, 3,072 tokens.
func showsInWindow(t *testing.T, text string) string {
	t.Helper()
	var buf bytes.Buffer
	w := nlog.New(&buf, "s")
	if _, err := w.Append(nlog.KindToolResult, nlog.ToolResult{ID: "c", Name: "shell-exec", OK: true, Value: text}); err != nil {
		t.Fatal(err)
	}
	evs, err := nlog.Read(&buf)
	if err != nil {
		t.Fatal(err)
	}
	return window.Build(evs, "sys", 0, window.Policy{HighWater: 1 << 20, LowWater: 1 << 19, MaxResultTokens: 3072, NamedCuts: true, WideReads: true}).Messages[1].Content
}

// A view is under what the window cuts, so the model reads it whole, marker
// and ends: as the JSON the tool returns, and as the plain text the shell
// habit gives it. The same output without a view is cut again.
func TestShellExecViewIsNotCutAgainByTheWindow(t *testing.T) {
	r := newViewRig(t)
	cases := map[string]string{
		"stdout":     emit(400),
		"long lines": `awk 'BEGIN { for (i = 1; i <= 400; i++) { s = ""; for (k = 0; k < 30; k++) s = s "0123456789"; printf "%03d %s end\n", i, s } }'`,
		"both":       `awk 'BEGIN { for (i = 1; i <= 400; i++) { printf "out %03d ........................................\n", i; printf "err %03d ........................................\n", i > "/dev/stderr" } }'`,
	}
	for name, script := range cases {
		p, _ := r.h.in.Lookup("shell-exec")
		v, err := r.h.in.Apply(scheme.WithOrigin(context.Background(), scheme.OriginModel), scheme.Value{Kind: scheme.KPrim, Prim: p}, []scheme.Value{scheme.Str("bash"), scheme.List(scheme.Str("-c"), scheme.Str(script))})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := scheme.ToJSON(v)
		var out shellOut
		json.Unmarshal(raw, &out)
		plain := fmt.Sprintf("exit %v\n%s", out.Status, out.Stdout)
		if out.Stderr != "" {
			plain += "\n[stderr]\n" + out.Stderr
		}
		for form, text := range map[string]string{"json": string(raw), "plain": plain} {
			if got := showsInWindow(t, text); got != text {
				t.Errorf("%s as %s: the window cut a view of %d bytes:\n%.300s", name, form, len(text), got)
			}
		}
		if !strings.Contains(plain, "not shown") {
			t.Errorf("%s: no marker", name)
		}
	}
	// The control: the same lines read with cat are not a view and are cut.
	os.WriteFile(filepath.Join(r.h.root, "data.txt"), []byte(joinLines(1, 400)), 0o644)
	whole := r.run(t, "cat data.txt")
	if got := showsInWindow(t, "exit 0\n"+whole.Stdout); !strings.Contains(got, "of this result left out here") {
		t.Fatal("the control was not cut by the window")
	}
}
