package genesis

import "errors"

var (
	ErrInvalidTarget      = errors.New("invalid target")
	ErrStateLockupMissing = errors.New("state lockup parameter missing")
)
