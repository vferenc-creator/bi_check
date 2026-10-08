//go:build windows

package winapi

import (
	"testing"
	"unsafe"
)

func TestFilterStringDoubleNul(t *testing.T) {
	p := filterString([]FileFilter{{"Minden fájl", "*.*"}, {"Excel", "*.xlsx;*.xls"}})
	var got []uint16
	for i := 0; ; i++ {
		c := *(*uint16)(unsafe.Add(unsafe.Pointer(p), i*2))
		got = append(got, c)
		if c == 0 && len(got) >= 2 && got[len(got)-2] == 0 {
			break
		}
		if i > 200 {
			t.Fatal("no double NUL terminator")
		}
	}
	want := "Minden fájl\x00*.*\x00Excel\x00*.xlsx;*.xls\x00\x00"
	if string(utf16Decode(got)) != want {
		t.Fatalf("got %q", string(utf16Decode(got)))
	}
}

func utf16Decode(u []uint16) []rune {
	r := make([]rune, len(u))
	for i, c := range u {
		r[i] = rune(c)
	}
	return r
}
