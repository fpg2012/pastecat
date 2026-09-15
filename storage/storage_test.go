package storage

import (
	"strings"
	"testing"
)

func strRepeat(s string) string {
	return strings.Repeat(s, idSize/2)
}

func TestNewID(t *testing.T) {
	for _, c := range []struct {
		in      string
		wantErr bool
	}{
		{"", true},
		{"hello", false},
		{"hello world", false},
		{"中文名字", false},
		{"a/b", false},
		{"deadbeef", false},
		{strings.Repeat("x", maxIDLength), false},
		{strings.Repeat("x", maxIDLength+1), true},
		{strings.Repeat("中", maxIDLength), false},
		{strings.Repeat("中", maxIDLength+1), true},
	} {
		got, err := NewID(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf(`NewID(%q) didn't error as expected`, c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf(`NewID(%q) errored unexpectedly`, c.in)
		} else if string(got) != c.in {
			t.Errorf(`NewID(%q) got %q`, c.in, got)
		}
	}
}

func TestIDFromString(t *testing.T) {
	for _, c := range [...]struct {
		in      string
		want    ID
		wantErr bool
	}{
		{"", "", true},
		{"invalidhex", "", true},
		{strings.Repeat("0", idSize-1), "", true},
		{strings.Repeat("0", idSize+1), "", true},
		{"0x123456", "", true},
		{strRepeat("00"), ID(strRepeat("00")), false},
		{strRepeat("0a"), ID(strRepeat("0a")), false},
		{strRepeat("0F"), ID(strRepeat("0F")), false},
		{strRepeat("ee"), ID(strRepeat("ee")), false},
	} {
		got, err := IDFromString(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf(`IDFromString("%s") didn't error as expected`, c.in)
			}
		} else if err != nil {
			t.Errorf(`IDFromString("%s") errored unexpectedly`, c.in)
		} else if got != c.want {
			t.Errorf(`IDFromString("%s") got %q, want %q`, c.in, got, c.want)
		}
	}
}

func TestIDString(t *testing.T) {
	for _, c := range []struct {
		in   ID
		want string
	}{
		{"deadbeef", "deadbeef"},
		{"hello world", "hello world"},
		{"中文", "中文"},
	} {
		if got := c.in.String(); got != c.want {
			t.Errorf(`ID(%q).String() got "%s", want "%s"`, c.in, got, c.want)
		}
	}
}

func TestRandomID(t *testing.T) {
	countFalse := func(count int) func(ID) bool {
		cur := 0
		return func(ID) bool {
			if cur >= count {
				return true
			}
			cur++
			return false
		}
	}
	for _, c := range []struct {
		available func(ID) bool
		wantErr   bool
	}{
		{func(id ID) bool { return true }, false},
		{func(id ID) bool { return false }, true},
		{countFalse(randTries - 1), false},
		{countFalse(randTries + 1), true},
	} {
		id, err := randomID(c.available)
		if c.wantErr {
			if err == nil {
				t.Errorf(`randomID() didn't error as expected`)
			}
			continue
		}
		if err != nil {
			t.Errorf(`randomID() errored unexpectedly`)
		}
		if _, err := IDFromString(id.String()); err != nil {
			t.Errorf(`randomID() returned a non-hex id: %q`, id)
		}
	}
}
