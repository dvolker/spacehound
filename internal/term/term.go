// Package term provides minimal raw-mode terminal support for macOS using
// only the standard library, plus a key decoder for ANSI input.
package term

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

// State is a saved terminal configuration.
type State struct{ t syscall.Termios }

func ioctl(fd int, req uint, arg unsafe.Pointer) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(req), uintptr(arg))
	if e != 0 {
		return e
	}
	return nil
}

// IsTerminal reports whether fd is a terminal.
func IsTerminal(fd int) bool {
	var t syscall.Termios
	return ioctl(fd, syscall.TIOCGETA, unsafe.Pointer(&t)) == nil
}

// MakeRaw puts fd into raw mode and returns the previous state.
func MakeRaw(fd int) (*State, error) {
	var old syscall.Termios
	if err := ioctl(fd, syscall.TIOCGETA, unsafe.Pointer(&old)); err != nil {
		return nil, errors.New("not a terminal")
	}
	raw := old
	raw.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	raw.Oflag &^= syscall.OPOST
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if err := ioctl(fd, syscall.TIOCSETA, unsafe.Pointer(&raw)); err != nil {
		return nil, err
	}
	return &State{old}, nil
}

// Restore returns fd to a saved state.
func Restore(fd int, s *State) error {
	if s == nil {
		return nil
	}
	return ioctl(fd, syscall.TIOCSETA, unsafe.Pointer(&s.t))
}

// Size returns the terminal width and height.
func Size(fd int) (w, h int, err error) {
	var ws struct{ Row, Col, X, Y uint16 }
	if err := ioctl(fd, syscall.TIOCGWINSZ, unsafe.Pointer(&ws)); err != nil {
		return 0, 0, err
	}
	if ws.Col == 0 || ws.Row == 0 {
		return 80, 24, nil
	}
	return int(ws.Col), int(ws.Row), nil
}

// Stdin returns the file descriptor of standard input.
func Stdin() int { return int(os.Stdin.Fd()) }

// Stdout returns the file descriptor of standard output.
func Stdout() int { return int(os.Stdout.Fd()) }
