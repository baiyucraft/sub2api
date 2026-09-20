package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/baiyucraft/codex-state-plugin/core"
	"github.com/baiyucraft/codex-state-plugin/rpcadapter"
	pluginv1 "github.com/baiyucraft/codex-state-plugin/sdk/v1"
)

func main() {
	version := flag.Bool("version", false, "print plugin version")
	flag.Parse()
	if *version {
		fmt.Println(core.Version)
		return
	}
	server := rpcadapter.New()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	}()
	pluginv1.Serve(server)
}
