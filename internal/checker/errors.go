package checker

import (
	"errors"
	"io/fs"
)

// errClass tells how a file system error should be treated.
type errClass int

const (
	errFileNotFound errClass = iota // the file (only) does not exist
	errPathNotFound                 // a folder on the way does not exist
	errAccess                       // permission problem for this item
	errServer                       // network/server problem → circuit breaker
	errOther
)

// classify maps an error to a class and a Hungarian explanation.
func classify(err error) (errClass, string) {
	if c, msg, ok := classifyOS(err); ok {
		return c, msg
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return errFileNotFound, "A fájl nem található."
	case errors.Is(err, fs.ErrPermission):
		return errAccess, "Hozzáférés megtagadva (jogosultsági hiba)."
	}
	return errOther, err.Error()
}
