package main

import "os/exec"

// hideCmd is a passthrough no-op kept so call sites read uniformly.
func hideCmd(cmd *exec.Cmd) *exec.Cmd { return cmd }
