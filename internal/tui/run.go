package tui

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"spacehound/internal/term"
)

// Run drives m on the controlling terminal until the user quits.
func Run(m *Model) error {
	in, out := term.Stdin(), term.Stdout()
	if !term.IsTerminal(in) || !term.IsTerminal(out) {
		return fmt.Errorf("the interactive UI needs a terminal; use --json for scripting")
	}
	state, err := term.MakeRaw(in)
	if err != nil {
		return err
	}
	// Alternate screen, hide cursor; undone on every exit path.
	fmt.Print("\x1b[?1049h\x1b[?25l")
	defer func() {
		fmt.Print("\x1b[0m\x1b[?25h\x1b[?1049l")
		term.Restore(in, state)
	}()

	msgs := make(chan Msg, 256)
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				close(msgs)
				return
			}
			for _, k := range term.Decode(buf[:n]) {
				msgs <- KeyMsg{k}
			}
		}
	}()
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	// Raw mode disables ISIG, but a SIGTERM/SIGHUP must still restore the TTY.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(stop)

	run := func(c Cmd) {
		if c == nil {
			return
		}
		go func() { msgs <- c(func(x Msg) { msgs <- x }) }()
	}
	w, h, _ := term.Size(out)
	m.Update(ResizeMsg{w, h})
	run(m.Init())
	for !m.Quit {
		fmt.Print("\x1b[H" + m.View(w, h))
		select {
		case msg, ok := <-msgs:
			if !ok {
				return nil
			}
			run(m.Update(msg))
		case <-winch:
			w, h, _ = term.Size(out)
			m.Update(ResizeMsg{w, h})
		case <-stop:
			if m.cancel != nil {
				m.cancel()
			}
			return fmt.Errorf("terminated by signal")
		}
	}
	if m.cancel != nil {
		m.cancel()
	}
	return nil
}
