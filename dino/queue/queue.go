package queue

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const (
	PriorityLow    = 0
	PriorityNormal = 1
	PriorityHigh   = 2
)

type Priority int

type Item struct {
	ID        string
	Content   string
	Priority  Priority
	CreatedAt time.Time
	Ch        chan *Result
}

type Result struct {
	Output   string
	Error    error
	Duration time.Duration
}

type Config struct {
	MaxSize    int
	MaxPending int
	Timeout    time.Duration
}

func DefaultConfig() *Config {
	return &Config{
		MaxSize:    1000,
		MaxPending: 10,
		Timeout:    5 * time.Minute,
	}
}

type Interface interface {
	Enqueue(ctx context.Context, content string, priority Priority) (<-chan *Result, error)
	EnqueueBatch(ctx context.Context, items []string, priority Priority) ([]<-chan *Result, error)
	OutputChan() <-chan *Item
	Complete(item *Item, result *Result)
	Size() int
	Pending() int
	GetStats() Stats
	Clear()
	Close()
}

type Stats struct {
	Size           int           `json:"size"`
	Pending        int           `json:"pending"`
	TotalEnqueued  int64         `json:"total_enqueued"`
	TotalProcessed int64         `json:"total_processed"`
	AvgWaitTime    time.Duration `json:"avg_wait_time"`
}

type queue struct {
	config     *Config
	inputChan  chan *Item
	outputChan chan *Item
	processing map[string]*Item
	mu         sync.RWMutex
	closed     bool
	stats      Stats
	waitTimes  []time.Duration
	done       chan struct{}
	doneWg     sync.WaitGroup
}

func New(cfg *Config) Interface {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	if cfg.MaxSize <= 0 {
		cfg.MaxSize = 1000
	}
	if cfg.MaxPending <= 0 {
		cfg.MaxPending = 10
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Minute
	}

	q := &queue{
		config:     cfg,
		inputChan:  make(chan *Item, cfg.MaxSize),
		outputChan: make(chan *Item, cfg.MaxPending),
		processing: make(map[string]*Item),
		stats:      Stats{},
		done:       make(chan struct{}),
	}
	q.doneWg.Add(1)
	go q.run()

	return q
}

func (q *queue) run() {
	defer q.doneWg.Done()
	for {
		select {
		case <-q.done:
			return
		case item := <-q.inputChan:
			q.mu.Lock()
			q.processing[item.ID] = item
			q.stats.Size = len(q.inputChan)
			q.stats.Pending = len(q.processing)
			q.mu.Unlock()
			select {
			case q.outputChan <- item:
			case <-q.done:
				return
			}
		}
	}
}

func (q *queue) Enqueue(ctx context.Context, content string, priority Priority) (<-chan *Result, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return nil, ErrClosed
	}

	if q.config.MaxSize > 0 && len(q.inputChan) >= q.config.MaxSize {
		return nil, ErrFull
	}

	if q.config.MaxPending > 0 && len(q.processing) >= q.config.MaxPending {
		return nil, ErrFull
	}

	item := &Item{
		ID:        generateID(),
		Content:   content,
		Priority:  priority,
		CreatedAt: time.Now(),
		Ch:        make(chan *Result, 1),
	}

	q.stats.TotalEnqueued++

	select {
	case q.inputChan <- item:
		return item.Ch, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (q *queue) EnqueueBatch(ctx context.Context, contents []string, priority Priority) ([]<-chan *Result, error) {
	results := make([]<-chan *Result, 0, len(contents))

	for _, content := range contents {
		ch, err := q.Enqueue(ctx, content, priority)
		if err != nil {
			return results, err
		}
		results = append(results, ch)
	}

	return results, nil
}

func (q *queue) OutputChan() <-chan *Item {
	return q.outputChan
}

func (q *queue) Complete(item *Item, result *Result) {
	q.mu.Lock()
	defer q.mu.Unlock()

	waitTime := time.Since(item.CreatedAt)
	q.stats.TotalProcessed++
	q.waitTimes = append(q.waitTimes, waitTime)
	if len(q.waitTimes) > 100 {
		q.waitTimes = q.waitTimes[1:]
	}

	if len(q.waitTimes) > 0 {
		var total time.Duration
		for _, t := range q.waitTimes {
			total += t
		}
		q.stats.AvgWaitTime = total / time.Duration(len(q.waitTimes))
	}

	delete(q.processing, item.ID)
	q.stats.Pending = len(q.processing)

	select {
	case item.Ch <- result:
	default:
	}
}

func (q *queue) Size() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return len(q.inputChan)
}

func (q *queue) Pending() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return len(q.processing)
}

func (q *queue) GetStats() Stats {
	q.mu.RLock()
	defer q.mu.RUnlock()
	stats := q.stats
	stats.Size = len(q.inputChan)
	stats.Pending = len(q.processing)
	return stats
}

func (q *queue) Clear() {
	q.mu.Lock()
	defer q.mu.Unlock()

DrainInput:
	for {
		select {
		case item := <-q.inputChan:
			select {
			case item.Ch <- &Result{Error: ErrCleared}:
			default:
			}
		default:
			break DrainInput
		}
	}

	for _, item := range q.processing {
		select {
		case item.Ch <- &Result{Error: ErrCleared}:
		default:
		}
	}

	q.processing = make(map[string]*Item)
	q.stats = Stats{}
}

func (q *queue) Close() {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	close(q.done)
	q.mu.Unlock()

	q.doneWg.Wait()
	close(q.outputChan)

	q.mu.Lock()
DrainCloseInput:
	for {
		select {
		case item := <-q.inputChan:
			select {
			case item.Ch <- &Result{Error: ErrClosed}:
			default:
			}
		default:
			break DrainCloseInput
		}
	}
	close(q.inputChan)
	for _, item := range q.processing {
		select {
		case item.Ch <- &Result{Error: ErrClosed}:
		default:
		}
	}
	q.processing = make(map[string]*Item)
	q.mu.Unlock()
}

type Error string

func (e Error) Error() string {
	return string(e)
}

var (
	ErrClosed  = Error("queue closed")
	ErrFull    = Error("queue is full")
	ErrEmpty   = Error("queue is empty")
	ErrCleared = Error("queue was cleared")
)

var queueIDCounter uint64

func generateID() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), atomic.AddUint64(&queueIDCounter, 1))
}
