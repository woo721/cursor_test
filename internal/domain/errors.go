package domain

import "errors"

var (
	ErrInvalidArgument       = errors.New("invalid argument")
	ErrDependencyUnavailable = errors.New("dependency unavailable")
	ErrOverflow              = errors.New("numeric overflow")
)
