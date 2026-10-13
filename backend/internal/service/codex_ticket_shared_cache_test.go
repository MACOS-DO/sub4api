package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/stretchr/testify/require"
)

// memoryCodexTicketCache is an in-memory CodexSharedTicketCache.
type memoryCodexTicketCache struct {
	mu        sync.Mutex
	values    map[string]*CodexSharedTicketState
	lockOwner map[string]string
	lockHeld  bool // simulates another instance holding every lock
	getErr    error
	deletes   atomic.Int32
	extends   atomic.Int32
}

func newMemoryCodexTicketCache() *memoryCodexTicketCache {
	return &memoryCodexTicketCache{values: map[string]*CodexSharedTicketState{}, lockOwner: map[string]string{}}
}

func (c *memoryCodexTicketCache) GetSharedTicket(_ context.Context) (*CodexSharedTicketState, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.getErr != nil {
		return nil, false, c.getErr
	}
	state, ok := c.values[""]
	return state, ok, nil
}

// SetSharedTicket mirrors the Redis script: an older version never replaces a newer one.
func (c *memoryCodexTicketCache) SetSharedTicket(_ context.Context, state *CodexSharedTicketState, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if current, ok := c.values[""]; ok && current.Version > state.Version {
		return nil
	}
	c.values[""] = state
	return nil
}

func (c *memoryCodexTicketCache) DeleteSharedTicket(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.values, "")
	c.deletes.Add(1)
	return nil
}

func (c *memoryCodexTicketCache) TryLock(_ context.Context, name, owner string, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lockHeld || c.lockOwner[name] != "" {
		return false, nil
	}
	c.lockOwner[name] = owner
	return true, nil
}

func (c *memoryCodexTicketCache) ExtendLock(_ context.Context, name, owner string, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.extends.Add(1)
	return c.lockOwner[name] == owner, nil
}

func (c *memoryCodexTicketCache) Unlock(_ context.Context, name, owner string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lockOwner[name] == owner {
		delete(c.lockOwner, name)
	}
	return nil
}

// countingTicketRepo counts database reads and can be slowed down or failed.
// Like the database row, every stored ticket bumps the version.
type countingTicketRepo struct {
	CodexTicketLifecycleRepository
	mu      sync.Mutex
	ticket  *openAICodexTicket
	version int64
	reads   atomic.Int32
	delay   time.Duration
	err     error
}

func (r *countingTicketRepo) CurrentTicket(context.Context) (*CodexSharedTicketState, error) {
	r.reads.Add(1)
	time.Sleep(r.delay)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	raw, err := json.Marshal(r.ticket)
	if err != nil {
		return nil, err
	}
	return &CodexSharedTicketState{Ticket: raw, Version: r.version}, nil
}

func (r *countingTicketRepo) StoreTicket(_ context.Context, _ string, _ int64, _ time.Time, raw json.RawMessage) error {
	var ticket openAICodexTicket
	if err := json.Unmarshal(raw, &ticket); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ticket = &ticket
	r.version++
	return nil
}

func (r *countingTicketRepo) setErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

func TestCodexSharedTicketRedisMissLoadsDatabaseOnce(t *testing.T) {
	ticket := verifiedTicket(ticketTestAccount(40), "gpt-6-astra", "shared-state", "__oailb=shared; __cflb=route")
	repo := &countingTicketRepo{ticket: ticket, delay: 20 * time.Millisecond}
	cache := newMemoryCodexTicketCache()
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
	svc.openaiCodexTicketLifecycle = repo
	svc.SetCodexSharedTicketCache(cache)

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			view, err := svc.loadSharedCodexTicketView()
			require.NoError(t, err)
			require.Equal(t, ticket.GenerationID, view.current().GenerationID)
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, repo.reads.Load(), "concurrent misses collapse into one database read")
	_, hit, _ := cache.GetSharedTicket(context.Background())
	require.True(t, hit, "the lock holder fills Redis")

	// A Redis hit never reaches the database.
	svc.openaiCodexTicket.Store(nil)
	_, err := svc.loadSharedCodexTicketView()
	require.NoError(t, err)
	require.EqualValues(t, 1, repo.reads.Load())
}

func TestCodexSharedTicketLockWaitFallsBackToDatabase(t *testing.T) {
	ticket := verifiedTicket(ticketTestAccount(40), "gpt-6-astra", "shared-state", "__oailb=shared; __cflb=route")
	repo := &countingTicketRepo{ticket: ticket}
	cache := newMemoryCodexTicketCache()
	cache.lockHeld = true
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
	svc.openaiCodexTicketLifecycle = repo
	svc.SetCodexSharedTicketCache(cache)

	start := time.Now()
	view, err := svc.loadSharedCodexTicketView()
	require.NoError(t, err)
	require.Equal(t, ticket.GenerationID, view.current().GenerationID)
	require.GreaterOrEqual(t, time.Since(start), codexSharedTicketLockWait)
	require.EqualValues(t, 1, repo.reads.Load())
	_, hit, _ := cache.GetSharedTicket(context.Background())
	require.False(t, hit, "only the lock holder writes Redis")
}

func TestCodexSharedTicketFailuresKeepLastKnownTicket(t *testing.T) {
	ticket := verifiedTicket(ticketTestAccount(40), "gpt-6-astra", "shared-state", "__oailb=shared; __cflb=route")
	repo := &countingTicketRepo{ticket: ticket}
	cache := newMemoryCodexTicketCache()
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
	svc.openaiCodexTicketLifecycle = repo
	svc.SetCodexSharedTicketCache(cache)
	account := ticketTestAccount(41)
	require.False(t, svc.openAICodexTicketBlocksAccount(account, ticket.Model))

	// Redis down: the background refresh reads the database directly.
	cache.getErr = errors.New("redis down")
	svc.openaiCodexTicket.Store(&openAICodexSharedTicketEntry{view: &codexSharedTicketView{ticket: ticket}, loadedAt: time.Now().Add(-time.Minute)})
	require.False(t, svc.openAICodexTicketBlocksAccount(account, ticket.Model))
	require.Eventually(t, func() bool { return repo.reads.Load() == 2 }, time.Second, 5*time.Millisecond)

	// Redis and the database both down: the last known ticket keeps the account schedulable.
	repo.setErr(errors.New("database down"))
	stale := &openAICodexSharedTicketEntry{view: &codexSharedTicketView{ticket: ticket}, loadedAt: time.Now().Add(-time.Minute)}
	svc.openaiCodexTicket.Store(stale)
	require.False(t, svc.openAICodexTicketBlocksAccount(account, ticket.Model))
	require.Eventually(t, func() bool {
		entry := svc.openaiCodexTicket.Load()
		return entry != stale && time.Since(entry.loadedAt) < codexSharedTicketLocalTTL
	}, time.Second, 5*time.Millisecond, "a failed refresh postpones the next one instead of retrying on every read")
	require.Same(t, ticket, svc.openaiCodexTicket.Load().view.current())
}

func TestCodexSharedTicketStaleReadRefreshesInBackgroundOnce(t *testing.T) {
	ticket := verifiedTicket(ticketTestAccount(40), "gpt-6-astra", "shared-state", "__oailb=shared; __cflb=route")
	repo := &countingTicketRepo{ticket: ticket, delay: 100 * time.Millisecond}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
	svc.openaiCodexTicketLifecycle = repo
	old := verifiedTicket(ticketTestAccount(39), ticket.Model, "old-state", "__oailb=old; __cflb=route")
	svc.openaiCodexTicket.Store(&openAICodexSharedTicketEntry{view: &codexSharedTicketView{ticket: old}, loadedAt: time.Now().Add(-time.Minute)})

	start := time.Now()
	for range 20 {
		require.Same(t, old, svc.sharedCodexTicketView().current(), "a stale value is served without waiting")
	}
	require.Less(t, time.Since(start), repo.delay)
	require.Eventually(t, func() bool {
		return svc.sharedCodexTicketView().current().GenerationID == ticket.GenerationID
	}, time.Second, 5*time.Millisecond)
	require.EqualValues(t, 1, repo.reads.Load(), "concurrent stale reads trigger a single refresh")
}

func TestCodexSharedTicketColdReadLoadsSynchronously(t *testing.T) {
	ticket := verifiedTicket(ticketTestAccount(40), "gpt-6-astra", "shared-state", "__oailb=shared; __cflb=route")
	repo := &countingTicketRepo{ticket: ticket}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
	svc.openaiCodexTicketLifecycle = repo
	require.Equal(t, ticket.GenerationID, svc.sharedCodexTicketView().current().GenerationID)
	require.EqualValues(t, 1, repo.reads.Load())
}

func TestCodexSharedTicketWritePublishesNewVersion(t *testing.T) {
	source := ticketTestAccount(40)
	ticket := verifiedTicket(source, "gpt-6-astra", "shared-state", "__oailb=shared; __cflb=route")
	repo := &countingTicketRepo{ticket: verifiedTicket(source, ticket.Model, "before-state", "__oailb=before; __cflb=route"), version: 1}
	cache := newMemoryCodexTicketCache()
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
	svc.openaiCodexTicketLifecycle = repo
	svc.SetCodexSharedTicketCache(cache)
	// A reader loaded version 1 before the write.
	before, err := repo.CurrentTicket(context.Background())
	require.NoError(t, err)
	staleView, err := parseCodexSharedTicketState(before)
	require.NoError(t, err)

	require.NoError(t, svc.storeOpenAICodexTicket(context.Background(), source, ticket))
	require.Equal(t, ticket.GenerationID, svc.sharedCodexTicketView().current().GenerationID, "this instance sees the new ticket at once")
	cached, hit, _ := cache.GetSharedTicket(context.Background())
	require.True(t, hit, "the write goes through to Redis instead of deleting it")
	require.Equal(t, int64(2), cached.Version)
	require.Zero(t, cache.deletes.Load())

	// The slow reader finishes after the write: neither cache takes its older version.
	require.NoError(t, cache.SetSharedTicket(context.Background(), before, time.Minute))
	cached, _, _ = cache.GetSharedTicket(context.Background())
	require.Equal(t, int64(2), cached.Version)
	require.Equal(t, ticket.GenerationID, svc.cacheSharedCodexTicketView(staleView).current().GenerationID)
	require.Equal(t, ticket.GenerationID, svc.sharedCodexTicketView().current().GenerationID)
}

func TestCodexSharedTicketWriteEvictsWhenReloadFails(t *testing.T) {
	source := ticketTestAccount(40)
	ticket := verifiedTicket(source, "gpt-6-astra", "shared-state", "__oailb=shared; __cflb=route")
	repo := &countingTicketRepo{ticket: ticket}
	cache := newMemoryCodexTicketCache()
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
	svc.openaiCodexTicketLifecycle = repo
	svc.SetCodexSharedTicketCache(cache)
	raw, err := json.Marshal(ticket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSharedTicket(context.Background(), &CodexSharedTicketState{Ticket: raw}, time.Minute))
	svc.cacheSharedCodexTicketView(&codexSharedTicketView{ticket: ticket})

	repo.setErr(errors.New("database down"))
	require.NoError(t, svc.writeSharedCodexTicket(context.Background(), func(context.Context) (bool, error) { return true, nil }))
	_, hit, _ := cache.GetSharedTicket(context.Background())
	require.False(t, hit)
	require.Nil(t, svc.openaiCodexTicket.Load())
}

// A write that leaves the shared state unchanged (a repeated or stale invalidation)
// neither reloads the database nor rewrites Redis.
func TestCodexSharedTicketUnchangedWriteSkipsPublish(t *testing.T) {
	ticket := verifiedTicket(ticketTestAccount(40), "gpt-6-astra", "shared-state", "__oailb=shared; __cflb=route")
	repo := &countingTicketRepo{ticket: ticket, version: 1}
	cache := newMemoryCodexTicketCache()
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
	svc.openaiCodexTicketLifecycle = repo
	svc.SetCodexSharedTicketCache(cache)

	require.NoError(t, svc.writeSharedCodexTicket(context.Background(), func(context.Context) (bool, error) { return false, nil }))
	require.Zero(t, repo.reads.Load())
	_, hit, _ := cache.GetSharedTicket(context.Background())
	require.False(t, hit)

	require.NoError(t, svc.writeSharedCodexTicket(context.Background(), func(context.Context) (bool, error) { return true, nil }))
	require.EqualValues(t, 1, repo.reads.Load())
	_, hit, _ = cache.GetSharedTicket(context.Background())
	require.True(t, hit)
}

// adoptingProbe simulates a probe that harvested a ticket: it goes through the round
// like the real attempt, so the batch loop sees exactly what was written.
func adoptingProbe(ctx context.Context, store func() error) (superseded bool, err error) {
	if round := codexTicketRoundFrom(ctx); round != nil {
		return round.adopt(store)
	}
	return false, store()
}

func TestCodexTicketHarvestRunsInBatchesUntilTicket(t *testing.T) {
	for _, test := range []struct {
		name       string
		batchSize  int
		succeedIDs map[int64]bool
		want       int
	}{
		{name: "first batch succeeds", batchSize: 10, succeedIDs: map[int64]bool{5: true}, want: 10},
		{name: "first batch fails", batchSize: 10, succeedIDs: map[int64]bool{11: true}, want: 12},
		{name: "every batch fails", batchSize: 5, want: 12},
		{name: "zero uses default", batchSize: 0, succeedIDs: map[int64]bool{1: true}, want: 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
			var mu sync.Mutex
			var probed []int64
			svc.openaiCodexTicketProbe = func(ctx context.Context, account *Account) bool {
				mu.Lock()
				probed = append(probed, account.ID)
				mu.Unlock()
				if !test.succeedIDs[account.ID] {
					return false
				}
				_, err := adoptingProbe(ctx, func() error { return nil })
				return err == nil
			}
			accounts := make([]Account, 12)
			for i := range accounts {
				accounts[i] = *ticketTestAccount(int64(i + 1))
			}
			got := svc.harvestCodexTicketInBatches(context.Background(), accounts, test.batchSize)
			require.Equal(t, test.want, got)
			require.Len(t, probed, test.want)
		})
	}
}

func TestCodexTicketHarvestAdoptsOneTicketPerRound(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
	var adopted, superseded atomic.Int32
	var wg sync.WaitGroup
	wg.Add(3)
	svc.openaiCodexTicketProbe = func(ctx context.Context, _ *Account) bool {
		wg.Done()
		wg.Wait() // every probe of the batch succeeds at the same time
		lost, err := adoptingProbe(ctx, func() error { return nil })
		require.NoError(t, err)
		if lost {
			superseded.Add(1)
		} else {
			adopted.Add(1)
		}
		return true
	}
	accounts := []Account{*ticketTestAccount(1), *ticketTestAccount(2), *ticketTestAccount(3), *ticketTestAccount(4)}
	require.Equal(t, 3, svc.harvestCodexTicketInBatches(context.Background(), accounts, 3))
	require.EqualValues(t, 1, adopted.Load())
	require.EqualValues(t, 2, superseded.Load())
}

// A failed write must not end the round through probes it superseded: the next
// finished ticket is written instead, and only a written ticket stops the batches.
func TestCodexTicketHarvestRoundRetriesAfterFailedWrite(t *testing.T) {
	t.Run("next ticket of the batch is written", func(t *testing.T) {
		svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
		var mu sync.Mutex
		var writes []int64
		order := make(chan struct{}, 1)
		order <- struct{}{}
		svc.openaiCodexTicketProbe = func(ctx context.Context, account *Account) bool {
			if account.ID == 2 {
				<-order // account 1 writes first
			}
			superseded, err := adoptingProbe(ctx, func() error {
				mu.Lock()
				defer mu.Unlock()
				writes = append(writes, account.ID)
				if account.ID == 1 {
					order <- struct{}{}
					return errors.New("database down")
				}
				return nil
			})
			return err == nil && !superseded
		}
		<-order
		accounts := []Account{*ticketTestAccount(1), *ticketTestAccount(2), *ticketTestAccount(3)}
		require.Equal(t, 2, svc.harvestCodexTicketInBatches(context.Background(), accounts, 2), "the second write succeeded, so the round stops")
		require.Equal(t, []int64{1, 2}, writes)
	})
	t.Run("every write fails continues with the next batch", func(t *testing.T) {
		svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
		var writes atomic.Int32
		svc.openaiCodexTicketProbe = func(ctx context.Context, _ *Account) bool {
			_, err := adoptingProbe(ctx, func() error {
				writes.Add(1)
				return errors.New("database down")
			})
			return err == nil
		}
		accounts := []Account{*ticketTestAccount(1), *ticketTestAccount(2), *ticketTestAccount(3)}
		require.Equal(t, 3, svc.harvestCodexTicketInBatches(context.Background(), accounts, 2))
		require.EqualValues(t, 3, writes.Load(), "no probe is superseded by a write that never happened")
	})
}

func TestCodexTicketHarvestSkipsWhileAnotherInstanceHarvests(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
	cache := newMemoryCodexTicketCache()
	svc.SetCodexSharedTicketCache(cache)
	var probes atomic.Int32
	svc.openaiCodexTicketProbe = func(context.Context, *Account) bool {
		probes.Add(1)
		return false
	}
	accounts := []Account{*ticketTestAccount(1)}
	_, err := cache.TryLock(context.Background(), codexTicketHarvestLockName, "other-instance", time.Minute)
	require.NoError(t, err)
	require.Zero(t, svc.harvestCodexTicketInBatches(context.Background(), accounts, 10))
	require.Zero(t, probes.Load())

	require.NoError(t, cache.Unlock(context.Background(), codexTicketHarvestLockName, "other-instance"))
	require.Equal(t, 1, svc.harvestCodexTicketInBatches(context.Background(), accounts, 10))
	require.EqualValues(t, 1, probes.Load())
	cache.mu.Lock()
	defer cache.mu.Unlock()
	require.Empty(t, cache.lockOwner, "the round releases its lock")
}

// The harvest lock is renewed for as long as the round runs, and a lost lock
// cancels the round instead of letting two instances harvest at once.
func TestCodexTicketHarvestLockHeartbeat(t *testing.T) {
	newService := func(cache *memoryCodexTicketCache) *OpenAIGatewayService {
		// A 1s attempt timeout and no margin keep the lock TTL, and so the heartbeat, short.
		margin := codexTicketHarvestLockMargin
		codexTicketHarvestLockMargin = 0
		t.Cleanup(func() { codexTicketHarvestLockMargin = margin })
		svc := ticketTestService(t, config.OpenAICodexTicketConfig{HarvestAttemptTimeoutSeconds: 1}, nil)
		svc.SetCodexSharedTicketCache(cache)
		return svc
	}
	t.Run("extends while held", func(t *testing.T) {
		cache := newMemoryCodexTicketCache()
		svc := newService(cache)
		lockCtx, release, ok := svc.acquireCodexTicketHarvestLock(context.Background())
		require.True(t, ok)
		require.Eventually(t, func() bool { return cache.extends.Load() > 0 }, 5*time.Second, 20*time.Millisecond)
		require.NoError(t, lockCtx.Err())
		release()
		cache.mu.Lock()
		require.Empty(t, cache.lockOwner)
		cache.mu.Unlock()
		require.Error(t, lockCtx.Err(), "release ends the round context")
	})
	t.Run("lost lock cancels the round", func(t *testing.T) {
		cache := newMemoryCodexTicketCache()
		svc := newService(cache)
		lockCtx, release, ok := svc.acquireCodexTicketHarvestLock(context.Background())
		require.True(t, ok)
		defer release()
		cache.mu.Lock()
		cache.lockOwner[codexTicketHarvestLockName] = "another-instance"
		cache.mu.Unlock()
		require.Eventually(t, func() bool { return lockCtx.Err() != nil }, 5*time.Second, 20*time.Millisecond)
	})
}

func TestCodexTicketCadenceBatchSizeDefaultsForOldSettings(t *testing.T) {
	repo := &codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{
		codexTicketCadenceSettingKey: `{"retry_min_seconds":20,"retry_max_seconds":40,"refresh_seconds":900}`,
	}}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
	svc.settingService = NewSettingService(repo, &config.Config{})
	require.Equal(t, CodexTicketCadence{RetryMinSeconds: 20, RetryMaxSeconds: 40, RefreshSeconds: 900, BatchSize: openAICodexTicketDefaultBatchSize}, svc.codexTicketCadence(context.Background()))

	require.NoError(t, svc.settingService.SetCodexTicketCadence(context.Background(), CodexTicketCadence{RetryMinSeconds: 20, RetryMaxSeconds: 40, RefreshSeconds: 900, BatchSize: 3}))
	require.Equal(t, 3, svc.codexTicketCadence(context.Background()).BatchSize)
	require.Error(t, svc.settingService.SetCodexTicketCadence(context.Background(), CodexTicketCadence{RetryMinSeconds: 20, RetryMaxSeconds: 40, BatchSize: codexTicketMaxBatchSize + 1}))
	require.Error(t, svc.settingService.SetCodexTicketCadence(context.Background(), CodexTicketCadence{RetryMinSeconds: 20, RetryMaxSeconds: 40}), "0 is only a read-time default, never a saved value")
}
