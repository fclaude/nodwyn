package core

import (
	"os"
	"os/exec"
	"slices"
	"testing"
)

// goneProcess is the id of a process that has ended.
func goneProcess(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// A nodwyn that was killed leaves its server container running and the
// networks of a networked one, which Docker's address pools do not hold for
// long (P2-052). The next process removes what a process that is gone left,
// before it starts its own, and leaves what a process still there owns.
func TestStartingAContainedServerSweepsWhatADeadNodwynLeft(t *testing.T) {
	w := newContainedWorld(t)
	w.fake.AddOrphan("nodwyn-notes-crashed", goneProcess(t))
	w.fake.AddOrphan("nodwyn-notes-elsewhere", os.Getpid())
	// The first start in this process, here the review of the server's tools,
	// sweeps.
	server := w.server()
	_, manifest, err := InspectMCP(w.options(), server)
	if err != nil {
		t.Fatal(err)
	}
	server.Manifest = manifest
	w.save(server)
	if running := w.fake.Running(); slices.Contains(running, "nodwyn-notes-crashed") || !slices.Contains(running, "nodwyn-notes-elsewhere") {
		t.Fatalf("running after the sweep: %v", running)
	}
	if nets := w.fake.Networks(); slices.Contains(nets, "nodwyn-notes-crashed-in") || !slices.Contains(nets, "nodwyn-notes-elsewhere-in") {
		t.Fatalf("networks after the sweep: %v", nets)
	}
	// Once per runtime and process: a session starting later asks nothing.
	before := len(w.fake.Find("ps"))
	w.session()
	if after := len(w.fake.Find("ps")); after != before {
		t.Fatalf("a second start swept again: %d ps calls, then %d", before, after)
	}
}
