package main

import (
	"context"
	"errors"
)

// runBake is implemented in M1 (seed → model → rank → bucketize → artifacts).
func runBake(ctx context.Context, args []string) error {
	_ = ctx
	_ = args
	return errors.New("bake: not implemented yet (M1)")
}
