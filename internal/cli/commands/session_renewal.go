package commands

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/kamichidu/go-nando/controlrpc"
)

func startSessionRenewal(parent context.Context, client *controlrpc.Client, sessionID string) (context.Context, func() error) {
	ctx, cancel := context.WithCancel(parent)
	stop := make(chan struct{})
	done := make(chan error, 1)
	var stopOnce sync.Once
	var stopErr error
	go func() {
		ticker := time.NewTicker(controlrpc.ResourceSessionRenewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				done <- nil
				return
			case <-ctx.Done():
				done <- nil
				return
			case <-ticker.C:
				if err := client.Call(ctx, controlrpc.MethodRenew, map[string]string{"session_id": sessionID}, nil); err != nil {
					cancel()
					if !errors.Is(err, context.Canceled) {
						done <- err
					} else {
						done <- nil
					}
					return
				}
			}
		}
	}()
	return ctx, func() error {
		stopOnce.Do(func() {
			close(stop)
			cancel()
			stopErr = <-done
		})
		return stopErr
	}
}
