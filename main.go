package main

import (
	"context"
	"os"

	"github.com/projecteru2/core/log"
	"github.com/projecteru2/core/types"

	"github.com/cocoonstack/epoch/cmd"
	"github.com/cocoonstack/epoch/internal/util"
)

func main() {
	ctx := context.Background()
	logLevel := util.FirstNonEmpty(os.Getenv("EPOCH_LOG_LEVEL"), "info")
	if err := log.SetupLog(ctx, &types.ServerLogConfig{Level: logLevel}, ""); err != nil {
		log.WithFunc("main").Fatalf(ctx, err, "setup log")
	}
	cmd.Execute()
}
