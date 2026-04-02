package main

import (
	"context"
	"os"

	"github.com/projecteru2/core/log"
	"github.com/projecteru2/core/types"

	"github.com/cocoonstack/epoch/cmd"
	"github.com/cocoonstack/epoch/utils"
)

func main() {
	ctx := context.Background()
	logLevel := utils.FirstNonEmpty(os.Getenv("EPOCH_LOG_LEVEL"), "info")
	if err := log.SetupLog(ctx, &types.ServerLogConfig{Level: logLevel}, ""); err != nil {
		log.WithFunc("main").Fatalf(ctx, err, "setup log: %v", err)
	}
	cmd.Execute()
}
