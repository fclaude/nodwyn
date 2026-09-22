// Command nibb-schema regenerates the documented local protocol contract.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/fclaude/nibb/internal/frontend/rpc"
)

func main() {
	out := flag.String("out", "", "output file (default stdout)")
	flag.Parse()
	data, err := rpc.WireSchema()
	if err == nil {
		data = append(data, '\n')
		if *out == "" {
			_, err = os.Stdout.Write(data)
		} else {
			err = os.WriteFile(*out, data, 0644)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
