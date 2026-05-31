package daemon

import (
	"sync"
	"testing"
	"time"
)

// TestConcurrentListAndMutate hammers the read path (ListSessions, which the
// `list` command and shell completion hit) concurrently with the write paths
// (NextSeq, SetCWD, marking commands done). Run with -race, this fails if
// session.seq / session.cwd are accessed without consistent locking — the
// data race behind the previous "list while exec" instability.
func TestConcurrentListAndMutate(t *testing.T) {
	base := t.TempDir()
	c := NewController(base)
	sess, err := NewSession(base)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Kill()
	c.AddSession(sess)
	c.PrimarySID = sess.SID

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Reader: ListSessions reads seq, cwd, pid, alive.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = c.ListSessions()
				}
			}
		}()
	}

	// Writers: bump seq, set cwd, register/finish commands.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				seq := sess.NextSeq()
				sess.SetCWD("/tmp/dir")
				sess.mu.Lock()
				sess.running[seq] = &CmdState{Seq: seq}
				sess.mu.Unlock()
				_ = sess.IsOldestRunning(seq)
				sess.GetCWD()
				sess.Seq()
				sess.IsAlive()
			}
		}
	}()

	time.Sleep(500 * time.Millisecond)
	close(stop)
	wg.Wait()
}
