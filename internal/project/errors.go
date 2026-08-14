package project

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidRoot     = errors.New("invalid project root")
	ErrUnsafePath      = errors.New("unsafe project path")
	ErrRecordNotFound  = errors.New("project record not found")
	ErrRecordConflict  = errors.New("project record conflict")
	ErrRecordIntegrity = errors.New("project record integrity failure")
	ErrFilesystem      = errors.New("project filesystem failure")
)

// DomainError keeps project failures classifiable with errors.Is without
// requiring callers to parse messages or expose absolute local paths.
type DomainError struct {
	Kind error
	Op   string
	Err  error
}

func (err *DomainError) Error() string {
	if err == nil {
		return "project operation failed"
	}
	return fmt.Sprintf("%s: %v", err.Op, err.Kind)
}

func (err *DomainError) Unwrap() []error {
	if err == nil {
		return nil
	}
	if err.Err == nil {
		return []error{err.Kind}
	}
	return []error{err.Kind, err.Err}
}

func domainError(kind error, op string, err error) error {
	return &DomainError{Kind: kind, Op: op, Err: err}
}
