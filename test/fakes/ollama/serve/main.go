// Command serve runs the fake Ollama on loopback and answers every chat
// request with the same plain reply, for the selfcheck (ADR 13): the
// candidate's model turn goes through the real client, window, log and
// replay without a paid model, a key or a machine on the network.
//
//	go build -o fakemodel ./test/fakes/ollama/serve
//	./fakemodel -reply READY    # prints "MODEL http://127.0.0.1:PORT"
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func main() {
	reply := flag.String("reply", "READY", "the reply to every request")
	flag.Parse()
	model := fake.NewFunc(func(fake.Request) fake.Turn { return fake.Turn{Content: *reply} })
	fmt.Printf("MODEL %s\n", model.URL)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	<-ctx.Done()
	stop()
	model.Close()
}
