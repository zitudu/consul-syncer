package main

import (
	"context"

	syncer "github.com/zitudu/consul-syncer"
)

var cfg *syncer.Config = syncer.NewConfig()

func main() {
	syncer, err := syncer.NewSyncer(cfg)
	if err != nil {
		panic(err)
	}
	if err := syncer.Sync(context.Background()); err != nil {
		panic(err)
	}
}
