package term

// Key is a decoded keypress. Named keys use the constants below; printable
// characters are returned as their rune.
type Key rune

const (
	KeyUp Key = -(iota + 1)
	KeyDown
	KeyLeft
	KeyRight
	KeyPgUp
	KeyPgDn
	KeyHome
	KeyEnd
	KeyEnter
	KeyEsc
	KeyBackspace
	KeyTab
	KeyCtrlC
	KeyCtrlU
	KeyUnknown
)

// Decode splits one read from a raw terminal into keys.
func Decode(b []byte) []Key {
	var out []Key
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == 0x1b:
			if i+1 >= len(b) {
				out = append(out, KeyEsc)
				i++
				continue
			}
			if b[i+1] != '[' && b[i+1] != 'O' {
				out = append(out, KeyEsc)
				i++
				continue
			}
			j := i + 2
			for j < len(b) && (b[j] < 0x40 || b[j] > 0x7e) {
				j++
			}
			if j >= len(b) {
				out = append(out, KeyUnknown)
				i = len(b)
				continue
			}
			seq := string(b[i+2 : j+1])
			out = append(out, csi(seq))
			i = j + 1
		case c == '\r' || c == '\n':
			out = append(out, KeyEnter)
			i++
		case c == 0x7f || c == 0x08:
			out = append(out, KeyBackspace)
			i++
		case c == '\t':
			out = append(out, KeyTab)
			i++
		case c == 0x03:
			out = append(out, KeyCtrlC)
			i++
		case c == 0x15:
			out = append(out, KeyCtrlU)
			i++
		case c < 0x20:
			out = append(out, KeyUnknown)
			i++
		default:
			r, n := decodeRune(b[i:])
			out = append(out, Key(r))
			i += n
		}
	}
	return out
}

func csi(seq string) Key {
	switch seq {
	case "A":
		return KeyUp
	case "B":
		return KeyDown
	case "C":
		return KeyRight
	case "D":
		return KeyLeft
	case "H", "1~", "7~":
		return KeyHome
	case "F", "4~", "8~":
		return KeyEnd
	case "5~":
		return KeyPgUp
	case "6~":
		return KeyPgDn
	}
	return KeyUnknown
}

func decodeRune(b []byte) (rune, int) {
	s := string(b)
	for _, r := range s {
		n := len(string(r))
		return r, n
	}
	return rune(b[0]), 1
}
