//go:build windows

package checker

import (
	"errors"
	"syscall"
)

// Windows error codes relevant for network shares.
const (
	errFileNotFoundW     = 2
	errPathNotFoundW     = 3
	errAccessDenied      = 5
	errBadNetPath        = 53
	errNetworkBusy       = 54
	errUnexpNetErr       = 59
	errNetNameDeleted    = 64
	errNetworkAccessDen  = 65
	errBadNetName        = 67
	errSemTimeout        = 121
	errInvalidName       = 123
	errNoNetOrBadPath    = 1222
	errNetworkUnreach    = 1231
	errHostUnreach       = 1232
	errLogonFailure      = 1326
	errAccountRestrict   = 1327
	errBadNetResponse    = 58
	errNotConnected      = 2250
	errNoLogonServers    = 1311
	errTrustFailure      = 1789
	errDevNotExist       = 55
	errSessionCredConfl  = 1219
	errConnectionAborted = 1236
)

func classifyOS(err error) (errClass, string, bool) {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return 0, "", false
	}
	switch errno {
	case errFileNotFoundW:
		return errFileNotFound, "A fájl nem található.", true
	case errPathNotFoundW:
		return errPathNotFound, "A mappa nem található.", true
	case errInvalidName:
		return errAccess, "Érvénytelen fájl- vagy mappanév.", true
	case errAccessDenied, errNetworkAccessDen:
		return errAccess, "Hozzáférés megtagadva (jogosultsági hiba).", true
	case errBadNetPath, errDevNotExist:
		return errServer, "A hálózati útvonal nem található (a szerver nem érhető el).", true
	case errBadNetName:
		return errServer, "A megosztás nem található a szerveren.", true
	case errLogonFailure, errAccountRestrict, errNoLogonServers, errTrustFailure, errSessionCredConfl:
		return errServer, "Bejelentkezési / hitelesítési hiba a megosztáson.", true
	case errNetworkUnreach, errHostUnreach, errNoNetOrBadPath, errNotConnected:
		return errServer, "A hálózat vagy a szerver nem érhető el.", true
	case errSemTimeout, errNetNameDeleted, errUnexpNetErr, errNetworkBusy, errBadNetResponse, errConnectionAborted:
		return errServer, "Hálózati hiba: a kapcsolat megszakadt vagy időtúllépés történt.", true
	}
	return errOther, errno.Error(), true
}
