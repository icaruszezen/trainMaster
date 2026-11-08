package dataversion

import "context"

// OWNER: backend-core agent. The builder: one build at a time, oldest
// queued first; restart recovery.

// builder holds the state of the build queue in this process.
type builder struct {
	wake chan struct{}
}

func (b *builder) init() { b.wake = make(chan struct{}, 1) }

// notify wakes the builder (non-blocking).
func (b *builder) notify() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// recoverBuilds runs at Start, before the builder: versions a previous run
// left building fail as interrupted and their folders are emptied.
func (s *Service) recoverBuilds(ctx context.Context) error {
	panic("dataversion: recoverBuilds not implemented")
}

// builderLoop builds queued versions one at a time until ctx ends.
func (s *Service) builderLoop(ctx context.Context) {
	panic("dataversion: builderLoop not implemented")
}
