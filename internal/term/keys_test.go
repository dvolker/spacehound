package term

import (
	"reflect"
	"testing"
)

func TestDecode(t *testing.T) {
	got := Decode([]byte("j\x1b[A\x1b[B\x1b[5~\r \x7f\x1bq\x03é"))
	want := []Key{'j', KeyUp, KeyDown, KeyPgUp, KeyEnter, ' ', KeyBackspace, KeyEsc, 'q', KeyCtrlC, 'é'}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if k := Decode([]byte{0x1b}); len(k) != 1 || k[0] != KeyEsc {
		t.Fatalf("bare escape: %v", k)
	}
}
