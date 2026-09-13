// Copyright (C) 2017-2026 The Rune Authors
// SPDX-License-Identifier: GPL-3.0-or-later
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or (at
// your option) any later version.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

package ide

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
)

// immediateFlusherTarget completes every async op from its own
// goroutine so the flusher's awaiter stays in flight long enough for a
// concurrent wait to block, which is the interleaving the workspace
// handler hits when the FS watcher starts a reload mid-drain.
type immediateFlusherTarget struct{}

func (immediateFlusherTarget) done() (<-chan error, error) {
	ch := make(chan error, 1)
	go debug.CapturePanicReport(func() {
		runtime.Gosched()
		ch <- nil
		close(ch)
	})
	return ch, nil
}

func (t immediateFlusherTarget) FlushTab(
	context.Context, browserapi.Handler,
) (<-chan error, error) {
	return t.done()
}

func (t immediateFlusherTarget) ForceFlushTab(
	context.Context, browserapi.Handler,
) (<-chan error, error) {
	return t.done()
}

func (t immediateFlusherTarget) OverwriteTab(
	context.Context, browserapi.Handler,
) (<-chan error, error) {
	return t.done()
}

func (t immediateFlusherTarget) ReloadTab(
	context.Context, browserapi.Handler,
) (<-chan error, error) {
	return t.done()
}

func (immediateFlusherTarget) Resource(
	workspaceapi.URI,
) (browserapi.Handler, bool) {
	return nil, false
}

// TestFlusherWaitConcurrentWithStart reproduces the workspace-handler
// race: the FS watcher goroutine starts a reload (flusher bookkeeping
// going 0 -> 1) while the drain helper waits for in-flight ops from
// another goroutine. Both must be safe to call concurrently.
func TestFlusherWaitConcurrentWithStart(t *testing.T) {
	f := newFlusher(immediateFlusherTarget{}, &fakeNotifications{},
		func(func()) bool { return true })
	uri, err := workspaceapi.ParseURI("memory:///race.go")
	require.NoError(t, err)

	const iterations = 500
	var done sync.WaitGroup
	done.Add(2)
	go debug.CapturePanicReport(func() {
		defer done.Done()
		for range iterations {
			_ = f.reloadAsync(uri, nil, nil)
		}
	})
	go debug.CapturePanicReport(func() {
		defer done.Done()
		for range iterations {
			f.wait()
		}
	})
	done.Wait()

	f.wait()
	assert.Zero(t, f.inFlightCount(),
		"every awaiter must be accounted for once wait returns")
}

func startControlledFlusherOp(
	t *testing.T, f *flusher, uri workspaceapi.URI, onSuccess func(),
) chan error {
	t.Helper()
	done := make(chan error, 1)
	require.NoError(t, f.startAsync(uri, opSave,
		func(context.Context) (<-chan error, error) { return done, nil }, onSuccess))
	return done
}

func TestFlusherAfterPendingCompletion(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want []string
	}{
		{name: "success", want: []string{"success", "after", "after worker"}},
		{name: "error", err: errors.New("save failed"), want: []string{"after", "after worker"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queued := make(chan func(), 1)
			f := newFlusher(nil, &fakeNotifications{}, func(fn func()) bool {
				queued <- fn
				return true
			})
			uri, err := workspaceapi.ParseURI("memory:///pending.go")
			require.NoError(t, err)
			var calls []string
			assert.False(t, f.afterPending(uri, func() { calls = append(calls, "idle") }))
			done := startControlledFlusherOp(t, f, uri, func() { calls = append(calls, "success") })
			require.True(t, f.afterPending(uri, func() { calls = append(calls, "after") }))
			assert.Empty(t, calls)

			done <- tc.err
			f.wait()
			assert.Zero(t, f.inFlightCount())
			assert.Empty(t, calls, "worker completion must not run deferred callbacks")
			require.True(t, f.afterPending(uri, func() { calls = append(calls, "after worker") }))
			(<-queued)()
			assert.Equal(t, tc.want, calls)
			assert.False(t, f.afterPending(uri, func() { calls = append(calls, "late") }))
			assert.Equal(t, tc.want, calls)
		})
	}
}

func TestFlusherAfterPendingOverlappingCompletions(t *testing.T) {
	for _, sameURI := range []bool{false, true} {
		name := "different URIs"
		if sameURI {
			name = "same URI"
		}
		t.Run(name, func(t *testing.T) {
			queued := make(chan func(), 2)
			f := newFlusher(nil, &fakeNotifications{}, func(fn func()) bool {
				queued <- fn
				return true
			})
			firstURI, err := workspaceapi.ParseURI("memory:///first.go")
			require.NoError(t, err)
			secondURI, err := workspaceapi.ParseURI("memory:///second.go")
			require.NoError(t, err)
			if sameURI {
				secondURI = firstURI
			}
			var calls []string
			first := startControlledFlusherOp(t, f, firstURI, func() { calls = append(calls, "first success") })
			require.True(t, f.afterPending(firstURI, func() { calls = append(calls, "first after") }))
			first <- nil
			f.wait()
			second := startControlledFlusherOp(t, f, secondURI, func() { calls = append(calls, "second success") })
			require.True(t, f.afterPending(secondURI, func() { calls = append(calls, "second after") }))
			second <- nil
			f.wait()
			assert.Empty(t, calls)

			(<-queued)()
			if sameURI {
				assert.Equal(t, []string{"first success"}, calls)
			} else {
				assert.Equal(t, []string{"first success", "first after"}, calls)
				assert.False(t, f.afterPending(firstURI, func() { t.Error("first URI still pending") }))
			}
			(<-queued)()
			if sameURI {
				assert.Equal(t, []string{"first success", "second success", "first after", "second after"}, calls)
			} else {
				assert.Equal(t, []string{"first success", "first after", "second success", "second after"}, calls)
			}
			assert.False(t, f.afterPending(firstURI, func() { t.Error("first URI still pending") }))
			assert.False(t, f.afterPending(secondURI, func() { t.Error("second URI still pending") }))
		})
	}
}

func TestFlusherAfterPendingReentrant(t *testing.T) {
	queued := make(chan func(), 1)
	f := newFlusher(nil, &fakeNotifications{}, func(fn func()) bool {
		queued <- fn
		return true
	})
	uri, err := workspaceapi.ParseURI("memory:///reentrant.go")
	require.NoError(t, err)
	first := startControlledFlusherOp(t, f, uri, nil)
	second := make(chan error, 1)
	var startErr error
	var oldPending, newPending, secondAfter bool
	require.True(t, f.afterPending(uri, func() {
		oldPending = f.afterPending(uri, func() {})
		startErr = f.startAsync(uri, opSave,
			func(context.Context) (<-chan error, error) { return second, nil }, nil)
		newPending = f.afterPending(uri, func() { secondAfter = true })
	}))
	first <- nil
	f.wait()
	callback := <-queued
	returned := make(chan struct{})
	go debug.CapturePanicReport(func() {
		defer close(returned)
		callback()
	})
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("deferred callback deadlocked while starting new work")
	}
	require.NoError(t, startErr)
	assert.False(t, oldPending, "old pending state must be deleted before callbacks run")
	assert.True(t, newPending)
	assert.False(t, secondAfter)
	second <- nil
	f.wait()
	assert.False(t, secondAfter)
	(<-queued)()
	assert.True(t, secondAfter)
	assert.False(t, f.afterPending(uri, func() { t.Error("URI still pending") }))
}

func TestFlusherAfterPendingReparsedRemoteURI(t *testing.T) {
	queued := make(chan func(), 1)
	f := newFlusher(nil, &fakeNotifications{}, func(fn func()) bool {
		queued <- fn
		return true
	})
	uri, err := workspaceapi.ParseURI("ssh://user@host/project/file.go")
	require.NoError(t, err)
	eventURI, err := workspaceapi.ParseURI(uri.String())
	require.NoError(t, err)
	done := startControlledFlusherOp(t, f, uri, nil)
	var called bool
	require.True(t, f.afterPending(eventURI, func() { called = true }))
	done <- nil
	f.wait()
	(<-queued)()
	assert.True(t, called)
}

func TestFlusherAfterPendingRejectedSchedule(t *testing.T) {
	f := newFlusher(nil, &fakeNotifications{}, func(func()) bool { return false })
	uri, err := workspaceapi.ParseURI("memory:///closed.go")
	require.NoError(t, err)
	var called bool
	done := startControlledFlusherOp(t, f, uri, func() { called = true })
	require.True(t, f.afterPending(uri, func() { called = true }))
	done <- nil
	f.wait()
	assert.False(t, called, "rejected UI work must not run on the worker")
	assert.Empty(t, f.pending, "rejected scheduling must release deferred callbacks")
}
