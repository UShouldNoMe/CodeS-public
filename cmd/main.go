package main

import (
	"CodeS/internal/App"
	"CodeS/internal/tui"
	"context"
	"fmt"
	"os"
	"os/signal"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	approvalBroker := tui.NewApprovalBroker()
	application, err := App.Build(ctx, App.BuildOptions{
		WorkspaceRoot: ".",
		MaxTurns:      100,
		Approver:      approvalBroker.Approve,
		ConfigPath:    "config.yaml",
	})
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "start Codes:", err)
		return
	}
	defer func() {
		if err := application.Close(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "close CodeS:", err)
		}
	}()

	if err := tui.Run(ctx, application, approvalBroker); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Codes:", err)
	}
}
