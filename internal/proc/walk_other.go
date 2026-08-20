//go:build !linux && !darwin

package proc

import (
	"context"
	"errors"
)

// ErrNoLocalWalk says this platform has no way to read its own process table here.
//
// It is a REFUSAL and not a stub returning an empty table: an empty table identifies no agent,
// which is indistinguishable from "the pane is not an agent" — and that answer, silently, is what
// makes every send refuse for want of an identity token with nothing on screen saying why. An
// error reaches the operator.
var ErrNoLocalWalk = errors.New("proc: no local process walk on this platform")

func Local() Walker { return localWalker{} }

type localWalker struct{}

func (localWalker) Walk(context.Context, []int) (map[int]int, error) { return nil, ErrNoLocalWalk }

func Snapshot() ([]Proc, error) { return nil, ErrNoLocalWalk }
