//go:build !unix

package sync

import "os/exec"

func isolate(cmd *exec.Cmd) {}
