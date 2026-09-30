// Package plain is the line-mode front-end: a plain reader/writer pair over
// the core, with no terminal control. It is what CI, selfcheck and
// `nodwyn --plain` use. The agent loop front-end arrives in B2; this file is
// the Scheme REPL used to try the language directly.
package plain

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/fclaude/nodwyn/internal/scheme"
)

// SchemeREPL reads forms from r, evaluates them in in, and writes results
// to w until EOF. Incomplete input continues on the next line. Errors are
// printed and do not end the session.
func SchemeREPL(ctx context.Context, in *scheme.Interp, r io.Reader, w io.Writer, prompt bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	var pending strings.Builder
	show := func(p string) {
		if prompt {
			fmt.Fprint(w, p)
		}
	}
	show("nodwyn> ")
	for sc.Scan() {
		if pending.Len() > 0 {
			pending.WriteByte('\n')
		}
		pending.WriteString(sc.Text())
		src := pending.String()
		forms, err := scheme.Read(src)
		if errors.Is(err, scheme.ErrIncomplete) {
			show("...>  ")
			continue
		}
		pending.Reset()
		if err != nil {
			fmt.Fprintf(w, "error: %v\n", err)
			show("nodwyn> ")
			continue
		}
		for _, f := range forms {
			v, err := in.Eval(ctx, f, in.Global())
			if err != nil {
				fmt.Fprintf(w, "error: %v\n", err)
				break
			}
			if v.Kind != scheme.KVoid {
				fmt.Fprintln(w, scheme.Write(v))
			}
		}
		show("nodwyn> ")
	}
	if prompt {
		fmt.Fprintln(w)
	}
	return sc.Err()
}
