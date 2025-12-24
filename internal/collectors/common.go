package collectors

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

func runCommand(ctx context.Context, cmd string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, cmd, args...)
	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf
	if err := c.Run(); err != nil {
		return buf.String(), fmt.Errorf("%w: %s", err, buf.String())
	}
	return buf.String(), nil
}

func ctxWithTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d == 0 {
		d = 15 * time.Second
	}
	return context.WithTimeout(parent, d)
}
