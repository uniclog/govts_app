package serverstatus

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"time"
)

type Status struct {
	ServerVersion string  `json:"-"`
	OnlineCount   *uint32 `json:"onlineCount"`
	Status        string  `json:"status"`
	UpdatedAt     int64   `json:"updatedAt"`
	LastAttemptAt int64   `json:"lastAttemptAt"`
}

type entry struct {
	Status
	cancel context.CancelFunc
}

// Cache only performs network work when Begin is explicitly requested.
type Cache struct {
	mu      sync.Mutex
	entries map[netip.AddrPort]*entry
	closed  bool
	batch   *Batch
}

func NewCache() *Cache { return &Cache{entries: make(map[netip.AddrPort]*entry)} }

func (c *Cache) Forget(address netip.AddrPort) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if value := c.entries[address]; value != nil && value.cancel != nil {
		value.cancel()
	}
	delete(c.entries, address)
}

func (c *Cache) Snapshot(address netip.AddrPort) Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	value := c.entries[address]
	if value == nil {
		return Status{Status: "unknown"}
	}
	result := value.Status
	if result.OnlineCount != nil {
		count := *result.OnlineCount
		result.OnlineCount = &count
	}
	return result
}

type job struct {
	address netip.AddrPort
	entry   *entry
	ctx     context.Context
	result  Status
}

type Batch struct {
	cache  *Cache
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	jobs   []job
}

// Begin reserves the single batch before the caller releases its history lock.
// Run must be called for every successful Begin, including an empty batch.
func (c *Cache) Begin(addresses []netip.AddrPort, current netip.AddrPort) (*Batch, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("проверка статусов остановлена")
	}
	if c.batch != nil {
		return nil, errors.New("обновление статусов уже выполняется")
	}
	ctx, cancel := context.WithCancel(context.Background())
	b := &Batch{cache: c, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	wanted := make(map[netip.AddrPort]bool, len(addresses))
	for _, address := range addresses {
		if !address.IsValid() || wanted[address] {
			continue
		}
		wanted[address] = true
		value := c.entries[address]
		if value == nil {
			value = &entry{Status: Status{Status: "unknown"}}
			c.entries[address] = value
		}
		if address == current {
			continue
		}
		queryCtx, queryCancel := context.WithCancel(ctx)
		value.cancel = queryCancel
		b.jobs = append(b.jobs, job{address: address, entry: value, ctx: queryCtx, result: value.Status})
	}
	for address := range c.entries {
		if !wanted[address] {
			delete(c.entries, address)
		}
	}
	c.batch = b
	return b, nil
}

func (b *Batch) Run() error {
	defer func() {
		b.cancel()
		b.cache.mu.Lock()
		b.cache.batch = nil
		close(b.done)
		b.cache.mu.Unlock()
	}()
	queue := make(chan int, len(b.jobs))
	for i := range b.jobs {
		queue <- i
	}
	close(queue)
	var workers sync.WaitGroup
	for range min(4, len(b.jobs)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range queue {
				work := &b.jobs[i]
				if work.ctx.Err() != nil {
					continue
				}
				work.result.LastAttemptAt = time.Now().UnixMilli()
				count, version, err := QueryDetails(work.ctx, work.address)
				if err == nil {
					work.result.OnlineCount = &count
					work.result.ServerVersion = version
					work.result.UpdatedAt = time.Now().UnixMilli()
					work.result.Status = "available"
				} else {
					work.result.Status = "unavailable"
				}
			}
		}()
	}
	workers.Wait()
	b.cache.mu.Lock()
	defer b.cache.mu.Unlock()
	if err := b.ctx.Err(); err != nil {
		return err
	}
	for _, work := range b.jobs {
		if work.ctx.Err() == nil && b.cache.entries[work.address] == work.entry {
			work.entry.Status = work.result
			work.entry.cancel = nil
		}
	}
	return nil
}

// Close prevents new work, cancels sockets and waits for the reserved batch.
func (c *Cache) Close() {
	c.mu.Lock()
	c.closed = true
	b := c.batch
	if b != nil {
		b.cancel()
	}
	c.mu.Unlock()
	if b != nil {
		<-b.done
	}
}
