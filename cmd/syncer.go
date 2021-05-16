package main

import (
	"context"
	"os/signal"
	"syscall"

	syncer "github.com/zitudu/consul-syncer"
)

var cfg *syncer.Config = syncer.NewConfig()

func main() {
	syncer, err := syncer.NewSyncer(cfg)
	if err != nil {
		panic(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM)
	defer cancel()
	if err := syncer.Sync(ctx); err != nil {
		panic(err)
	}
}
