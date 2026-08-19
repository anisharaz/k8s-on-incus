package jobs

import (
	"fmt"
	"log"
	"runtime/debug"
)

// recoverJobPanic must be deferred at the top of every job goroutine. An
// unrecovered panic in any goroutine crashes the entire Go process (Go's
// default behavior isn't scoped to the goroutine that panicked) — one bad
// Incus API response or nil dereference deep in a single user's job would
// otherwise take down every other in-flight job, HTTP request, and open
// terminal session. Converts a panic into a normal job/resource failure via
// onFailed instead.
func recoverJobPanic(onFailed func(err error)) {
	if r := recover(); r != nil {
		err := fmt.Errorf("panic: %v", r)
		log.Printf("job goroutine panicked: %v\n%s", r, debug.Stack())
		onFailed(err)
	}
}
