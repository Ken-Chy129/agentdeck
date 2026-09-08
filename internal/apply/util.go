package apply

import (
	"bytes"
	"context"
	"os/exec"
	"time"
)

func lookPath(name string) (string, error) { return exec.LookPath(name) }

func runShell(shell string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return out.String(), err
}
