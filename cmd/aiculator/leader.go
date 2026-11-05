package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// standbyPoll is how often a standby tries the lock. Takeover costs at most this much on
// top of the store's own startup.
const standbyPoll = 200 * time.Millisecond

// lead makes this process the one writer for the data directory.
//
// The leader is whoever holds the lock on the data directory, not whoever answers health
// checks. The kernel drops the lock only when the process is gone, so a leader that is
// paused, swapped out or slow still holds it and no standby can start writing beside it.
// Electing on a missed health check would let a paused leader wake up next to a new one;
// spec/tla/Election.tla shows both versions, and the health one breaking.
//
// Without standby, a held lock is an error, as before. With it, the process waits until the
// lock comes free. A flock on a network filesystem is not reliable, so this covers servers
// on one machine sharing a local directory, not servers on several machines.
func lead(ctx context.Context, standby bool, log *slog.Logger) (unlock func(), term uint64, err error) {
	unlock, err = lockDataDir(dataDir)
	if err != nil && standby {
		pidPath := filepath.Join(dataDir, "standby.pid")
		_ = atomicWrite(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600)
		log.Info("standing by", "reason", err.Error())
		t := time.NewTicker(standbyPoll)
		for err != nil {
			select {
			case <-ctx.Done():
				removeIfMine(pidPath)
				return nil, 0, ctx.Err()
			case <-t.C:
			}
			unlock, err = lockDataDir(dataDir)
		}
		t.Stop()
		removeIfMine(pidPath)
	}
	if err != nil {
		return nil, 0, err
	}
	// The term only moves while the lock is held, so two leaders never share one.
	term = readTerm() + 1
	if err := atomicWrite(filepath.Join(dataDir, "term"), []byte(strconv.FormatUint(term, 10)), 0o600); err != nil {
		unlock()
		return nil, 0, err
	}
	log.Info("became leader", "term", term, "standby", standby)
	return unlock, term, nil
}

func readTerm() uint64 {
	b, err := os.ReadFile(filepath.Join(dataDir, "term"))
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	return n
}

// removeIfMine removes a pid file only if it names this process, so a standby leaving
// does not remove the file of the one that replaced it.
func removeIfMine(path string) {
	if pidFile(path) == os.Getpid() {
		_ = os.Remove(path)
	}
}

func pidFile(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}
