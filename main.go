package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/projecteru2/core/log"
	"github.com/projecteru2/core/types"

	"github.com/cocoonstack/epoch/cmd"
	"github.com/cocoonstack/epoch/utils"
)

func main() {
	ctx := context.Background()
	logLevel := utils.FirstNonEmpty(os.Getenv("EPOCH_LOG_LEVEL"), "info")
	if err := log.SetupLog(ctx, &types.ServerLogConfig{Level: logLevel}, ""); err != nil {
		log.WithFunc("main.main").Fatalf(ctx, err, "setup log: %v", err)
	}
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	cmd.Execute(ctx)
}
