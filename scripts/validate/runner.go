package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// result is one line of the summary. A failure carries the end of its
// output, so the reader sees why without opening the log.
type result struct {
	status string // PASS, FAIL, WARN or SKIP
	name   string
	detail string
	log    string
	tail   string
}

// Each command leads a process group of its own, which a signal to
// validate's group does not reach, so stopping validate stops it by hand.
// running is the set of groups run is waiting for (checks run at the same
// time) and stopping says a signal came: under mu, a command either starts
// before the signal and is stopped, or does not start.
var (
	mu       sync.Mutex
	running  = map[int]bool{}
	stopping bool
)

// stopOnSignal stops every running command's group when validate is
// interrupted or stopped, then exits. shell-stop sends SIGTERM and SIGKILL
// two seconds later, so the groups get their own SIGTERM and one second,
// all at once.
func stopOnSignal() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-signals
		mu.Lock()
		stopping = true
		groups := make([]int, 0, len(running))
		for group := range running {
			groups = append(groups, group)
		}
		mu.Unlock()
		var wg sync.WaitGroup
		for _, group := range groups {
			wg.Add(1)
			go func() {
				defer wg.Done()
				stopGroup(group)
			}()
		}
		wg.Wait()
		os.Exit(130)
	}()
}

// concurrently runs the jobs at the same time and returns their results in
// the jobs' order, so the summary does not depend on which finished first.
func concurrently(jobs ...func() []result) []result {
	parts := make([][]result, len(jobs))
	var wg sync.WaitGroup
	for i, job := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			parts[i] = job()
		}()
	}
	wg.Wait()
	var all []result
	for _, p := range parts {
		all = append(all, p...)
	}
	return all
}

// stopGroup sends a process group SIGTERM, and SIGKILL to what is left a
// second later. The SIGTERM matters: a command that starts groups of its
// own, as run.sh does for each browser check (and Playwright for
// Chromium), stops them from its handler, and SIGKILL runs no handler.
func stopGroup(group int) {
	if syscall.Kill(group, syscall.SIGTERM) != nil {
		return
	}
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if syscall.Kill(group, 0) != nil {
			return
		}
	}
	_ = syscall.Kill(group, syscall.SIGKILL)
}

// run starts a command in dir as the leader of a process group of its own,
// writes its output to logPath, waits at most limit, and then kills the
// whole group. A check that fails can leave a server it started running,
// and a pipe that server holds keeps whoever reads the pipe waiting: the
// browser checks' demo did that to a session for ten minutes on
// 2026-10-03. Output goes to a file, so nothing here waits on a pipe.
func run(dir, logPath string, limit time.Duration, name string, args ...string) (exit int, timedOut bool, err error) {
	f, err := os.Create(logPath)
	if err != nil {
		return -1, false, err
	}
	defer f.Close()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	mu.Lock()
	if stopping {
		mu.Unlock()
		return -1, false, errors.New("validate was stopped")
	}
	if err := cmd.Start(); err != nil {
		mu.Unlock()
		return -1, false, err
	}
	group := -cmd.Process.Pid
	running[group] = true
	mu.Unlock()
	defer func() {
		mu.Lock()
		delete(running, group)
		mu.Unlock()
	}()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-time.After(limit):
		timedOut = true
		stopGroup(group)
		err = <-done
	}
	// Whatever the command left behind in its group goes with it.
	stopGroup(group)
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, timedOut, nil
	case errors.As(err, &exitErr):
		return exitErr.ExitCode(), timedOut, nil
	default:
		return -1, timedOut, err
	}
}

// step runs one command as a named check and turns its exit into a result.
func step(name, detail, logs string, limit time.Duration, cmd string, args ...string) result {
	logPath := filepath.Join(logs, logName(name))
	start := time.Now()
	exit, timedOut, err := run(".", logPath, limit, cmd, args...)
	took := time.Since(start).Round(time.Second)
	r := result{name: name, detail: fmt.Sprintf("%s (%s)", detail, took), log: logPath}
	switch {
	case err != nil:
		r.status, r.detail = "FAIL", fmt.Sprintf("could not run %s: %v", cmd, err)
	case timedOut:
		r.status, r.detail = "FAIL", fmt.Sprintf("%s: stopped after %s", detail, limit)
	case exit != 0:
		r.status, r.detail = "FAIL", fmt.Sprintf("%s: exit %d (%s)", detail, exit, took)
	default:
		r.status = "PASS"
	}
	if r.status == "FAIL" {
		r.tail = lastLines(logPath, 25)
	}
	return r
}

func logName(name string) string {
	return strings.NewReplacer(" ", "-", "/", "-").Replace(name) + ".log"
}

// lastLines is the end of a log, for a failure's summary.
func lastLines(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > 64<<10 {
		_, _ = f.Seek(-64<<10, io.SeekEnd)
	}
	raw, _ := io.ReadAll(f)
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// summarise prints the results, failures first, and returns the exit code:
// 1 when anything failed.
func summarise(w io.Writer, results []result) int {
	failed, warned := 0, 0
	for _, status := range []string{"FAIL", "WARN", "SKIP", "PASS"} {
		for _, r := range results {
			if r.status != status {
				continue
			}
			fmt.Fprintf(w, "%-4s  %-22s %s\n", r.status, r.name, r.detail)
			if r.status == "FAIL" {
				failed++
				if r.log != "" {
					fmt.Fprintf(w, "      log: %s\n", r.log)
				}
				for _, line := range strings.Split(r.tail, "\n") {
					if line != "" {
						fmt.Fprintf(w, "      | %s\n", line)
					}
				}
			}
			if r.status == "WARN" {
				warned++
			}
		}
	}
	if failed > 0 {
		fmt.Fprintf(w, "validate: FAIL (%d failed, %d warnings)\n", failed, warned)
		return 1
	}
	fmt.Fprintf(w, "validate: PASS (%d warnings)\n", warned)
	return 0
}
