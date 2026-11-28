package orders

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kdraigo/dev_sdk/orderplan"
	"github.com/kdraigo/dev_sdk/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// One executor per account on a computer (wiki bug #76): the first to take
// the lock places; another stands by, knowing who places, and takes over
// when the lock is let go. Another account on the computer has its own
// (bug #65).
func TestExecutorLock(t *testing.T) {
	dir := t.TempDir()
	first, h, err := TryLockExecutor(dir, "key-wallet")
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Equal(t, os.Getpid(), h.PID)
	assert.NotEmpty(t, h.Token)

	second, held, err := TryLockExecutor(dir, "key-wallet")
	require.NoError(t, err)
	assert.Nil(t, second, "held: the second stands by")
	assert.Equal(t, h.Token, held.Token, "and knows who places")

	google, _, err := TryLockExecutor(dir, "key-google")
	require.NoError(t, err)
	require.NotNil(t, google, "another account's executor is its own")
	google.Release()

	// Let go with no one to take over: the holder hears it.
	first.Release()
	first.Release() // twice is harmless
	assert.False(t, first.TakenOver(60*time.Millisecond))

	// Let go with a standby watching: it takes over, and the holder hears it.
	again, _, err := TryLockExecutor(dir, "key-wallet")
	require.NoError(t, err)
	require.NotNil(t, again)
	taken := make(chan *ExecutorLock, 1)
	go func() {
		for {
			if l, _, _ := TryLockExecutor(dir, "key-wallet"); l != nil {
				taken <- l
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	time.Sleep(20 * time.Millisecond)
	again.Release()
	assert.True(t, again.TakenOver(time.Second), "a standby took over")
	successor := <-taken
	now, err := ReadHolder(dir, "key-wallet")
	require.NoError(t, err)
	assert.Equal(t, successor.token, now.Token)
	successor.Release()
}

// A standby proposes, and its checks read the console file of the process
// that places, as it is at each check: what that one opened since is AI
// orders' to act on, and what nobody opened stays refused (D4).
func TestStandbyReadsTheHoldersMemory(t *testing.T) {
	e, srv, x := rig(t)
	path := filepath.Join(t.TempDir(), "ai-orders-console.json")
	holder, _ := openConsole(t, path)
	e.StandBy(path, Holder{PID: 4242})
	standby, h := e.Standby()
	assert.True(t, standby)
	assert.Equal(t, 4242, h.PID)
	ctx := context.Background()

	x.positions = []*types.Position{{Symbol: "ETHUSDT", Side: "LONG", Size: 0.5, EntryPrice: 3000}}
	_, _, err := e.Propose(ctx, ProposeInput{DrawingID: "ai_1", EntryType: orderplan.AtMarket})
	assert.ErrorContains(t, err, "this exchange account holds a long ETHUSDT position of 0.5 that this computer did not place")
	_, _, err = e.ProposeAction(ctx, ActionInput{Kind: KindClose, Pair: "ETHUSDT"})
	assert.ErrorContains(t, err, "this computer did not open the ETHUSDT position")
	hs, err := e.Holdings(ctx, "ETHUSDT")
	require.NoError(t, err)
	require.Len(t, hs, 1)
	assert.True(t, hs[0].NotMine)

	// The process that places opened it: the standby sees it at once.
	holder.Track(&types.Order{ID: "e-1", Symbol: "ETHUSDT", Status: types.OrderStatusFilled})
	_, _, err = e.ProposeAction(ctx, ActionInput{Kind: KindClose, Pair: "ETHUSDT"})
	require.NoError(t, err)
	hs, err = e.Holdings(ctx, "ETHUSDT")
	require.NoError(t, err)
	assert.False(t, hs[0].NotMine)
	assert.Len(t, srv.proposed, 1, "the close was proposed")

	// A memory that cannot be read refuses rather than guess.
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o600))
	_, _, err = e.ProposeAction(ctx, ActionInput{Kind: KindClose, Pair: "ETHUSDT"})
	assert.ErrorContains(t, err, "the AI orders' memory could not be read")
}

// The process that places tells the live console "stopped" as it ends, only
// when no other kdraigo-mcp on the computer took over (the false stop notice
// of wiki bug #76).
func TestConsoleHandover(t *testing.T) {
	for _, handedOver := range []bool{true, false} {
		e, _, _ := rig(t)
		c, pub := openConsole(t, filepath.Join(t.TempDir(), "c.json"))
		e.WithConsole(c)
		asked := 0
		e.OnStop(func() bool { asked++; return handedOver })
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			e.runConsole(ctx)
		}()
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the console did not stop")
		}
		assert.Equal(t, 1, asked)
		pub.mu.Lock()
		stopped := len(pub.stopped)
		pub.mu.Unlock()
		if handedOver {
			assert.Zero(t, stopped, "handed over: the session runs on")
			assert.False(t, e.StopReported())
		} else {
			assert.Equal(t, 1, stopped)
			assert.True(t, e.StopReported())
		}
	}
}
