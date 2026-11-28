package tools

import (
	"context"
	"io"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kdraigo/kdraigo_mcp/internal/orders"
)

// waitingServer is kdraigo with an order stream that stays open until the
// executor stops; nothing else is asked of it here.
type waitingServer struct{ orders.Server }

func (waitingServer) Events(ctx context.Context, _ string) (io.ReadCloser, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// A kdraigo-mcp that stands by says who places the orders (wiki bug #76).
func TestOrders_StandbySetupText(t *testing.T) {
	a := &AIOrders{Path: "/home/u/.kdraigo/orders.yaml",
		Exec: orders.New(&orders.Config{Venue: "binance_futures", Network: "testnet"}, waitingServer{}, nil, nil, "pc-test", nil)}
	a.Exec.StandBy("/home/u/.kdraigo/ai-orders-console-x.json", orders.Holder{PID: 4242, Started: time.Date(2026, 11, 23, 8, 9, 0, 0, time.UTC)})
	s := server.NewMCPServer("test", "0")
	RegisterOrders(s, Deps{Orders: a})
	out, isErr := call(t, s, "order_setup", nil)
	require.False(t, isErr)
	assert.Contains(t, out, "One executor per computer: another kdraigo-mcp on this computer (process 4242, another Claude Code session's, running since 2026-11-23 08:09 UTC) places the confirmed orders.")
	assert.NotContains(t, out, "does not hear this computer", "a standby is not a broken connection")
}

// A standby takes over when the process that places lets the lock go (its
// Claude Code session closed), reading the journal and console then; it
// ends when its own context does.
func TestOrders_StandbyTakesOver(t *testing.T) {
	dir := t.TempDir()
	holder, _, err := orders.TryLockExecutor(dir, "key-wallet")
	require.NoError(t, err)
	require.NotNil(t, holder)

	a := &AIOrders{Exec: orders.New(&orders.Config{Venue: "binance_futures", Network: "testnet"}, waitingServer{}, nil, nil, "pc-test", nil), done: make(chan struct{})}
	a.Exec.StandBy(orders.AccountConsoleFile(dir, "key-wallet"), orders.Holder{})
	var opened atomic.Int32
	open := func() (*orders.Journal, *orders.Console, error) {
		opened.Add(1)
		j, err := orders.OpenJournal(filepath.Join(dir, "orders-journal.jsonl"))
		return j, nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		defer close(a.done)
		a.standBy(ctx, dir, "key-wallet", open, t.Logf)
	}()

	time.Sleep(3 * standbyPoll)
	standby, _ := a.Exec.Standby()
	assert.True(t, standby, "held by another: stands by")
	assert.Zero(t, opened.Load(), "nothing read while standing by")

	holder.Release()
	assert.Eventually(t, func() bool { s, _ := a.Exec.Standby(); return !s }, 2*time.Second, 10*time.Millisecond, "took over")
	assert.True(t, holder.TakenOver(time.Second), "the holder that left hears it")
	assert.Equal(t, int32(1), opened.Load())

	cancel()
	assert.False(t, a.Wait(2*time.Second), "no console: nothing told \"stopped\"")
	select {
	case <-a.done:
	default:
		t.Fatal("the executor did not stop with its context")
	}
	again, _, err := orders.TryLockExecutor(dir, "key-wallet")
	require.NoError(t, err)
	assert.NotNil(t, again, "let go as it stopped")
	again.Release()
}
