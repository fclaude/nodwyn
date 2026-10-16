package container

import (
	"context"
	"strconv"
	"strings"
)

// OwnerLabel is the label every container and network nodwyn makes carries:
// the process id of the nodwyn that made it. A nodwyn that is killed
// (SIGKILL, power loss) cannot remove what it made, and a server that
// ignores the end of its input keeps running, so the next one sweeps what a
// process that is gone left behind.
const OwnerLabel = "nodwyn.pid"

// Sweep removes the containers and then the networks that carry OwnerLabel
// for a process alive says is gone, and returns their names. It never
// touches one whose owner is still there, so a terminal and a daemon on the
// same computer do not remove each other's, and it ignores what a runtime
// cannot tell it: leftovers are no reason to fail a task.
func (r Runtime) Sweep(ctx context.Context, alive func(pid int) bool) []string {
	var removed []string
	gone := func(listing, inspect []string) []string {
		out, err := r.output(ctx, listing...)
		if err != nil {
			return nil
		}
		var names []string
		for _, name := range strings.Fields(out) {
			label, err := r.output(ctx, append(append([]string(nil), inspect...), name)...)
			if err != nil {
				continue
			}
			if pid, err := strconv.Atoi(strings.TrimSpace(label)); err == nil && pid > 0 && !alive(pid) {
				names = append(names, name)
			}
		}
		return names
	}
	filter := "label=" + OwnerLabel
	index := `{{index .Config.Labels "` + OwnerLabel + `"}}`
	if containers := gone([]string{"ps", "-a", "--filter", filter, "--format", "{{.Names}}"}, []string{"container", "inspect", "--format", index}); len(containers) > 0 {
		if _, err := r.output(ctx, r.rmArgs(containers...)...); err == nil {
			removed = append(removed, containers...)
		}
	}
	index = `{{index .Labels "` + OwnerLabel + `"}}`
	for _, network := range gone([]string{"network", "ls", "--filter", filter, "--format", "{{.Name}}"}, []string{"network", "inspect", "--format", index}) {
		if _, err := r.output(ctx, "network", "rm", network); err == nil {
			removed = append(removed, network)
		}
	}
	return removed
}
