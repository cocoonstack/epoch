package main

import (
	"context"
	"os/signal"
	"syscall"

	commonlog "github.com/cocoonstack/cocoon-common/log"

	"github.com/cocoonstack/epoch/cmd"
)

func main() {
	ctx := context.Background()
	commonlog.Setup(ctx, "EPOCH_LOG_LEVEL")
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	cmd.Execute(ctx)
}
