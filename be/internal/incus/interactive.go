package incus

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/gorilla/websocket"
	incusclient "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// ExecInteractive starts an interactive PTY session (bash) inside the named
// instance, streaming stdin/stdout and forwarding resize events (each a
// [width, height] pair) until ctx is done or the session ends. Unlike Exec,
// this opens a single combined PTY stream — there's no separate stderr.
func (c *Client) ExecInteractive(ctx context.Context, name string, stdin io.Reader, stdout io.Writer, resize <-chan [2]int) error {
	req := api.InstanceExecPost{
		Command:     []string{"bash"},
		Environment: map[string]string{"TERM": "xterm-256color"},
		WaitForWS:   true,
		Interactive: true,
		Width:       100,
		Height:      30,
	}

	dataDone := make(chan bool)
	args := incusclient.InstanceExecArgs{
		Stdin:  stdin,
		Stdout: stdout,
		Control: func(control *websocket.Conn) {
			for {
				select {
				case wh, ok := <-resize:
					if !ok {
						return
					}
					_ = control.WriteJSON(api.InstanceExecControl{
						Command: "window-resize",
						Args: map[string]string{
							"width":  strconv.Itoa(wh[0]),
							"height": strconv.Itoa(wh[1]),
						},
					})
				case <-ctx.Done():
					return
				}
			}
		},
		DataDone: dataDone,
	}

	op, err := callWithContext(ctx, func() (incusclient.Operation, error) {
		return c.server.ExecInstance(name, req, &args)
	})
	if err != nil {
		return fmt.Errorf("interactive exec in instance %q: %w", name, err)
	}

	waitErr := op.WaitContext(ctx)
	<-dataDone

	if waitErr != nil {
		return fmt.Errorf("interactive exec in instance %q: %w", name, waitErr)
	}

	return nil
}
