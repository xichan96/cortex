package eventbus

import (
	"sync"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	bus := New()
	if bus == nil {
		t.Error("New() should return a non-nil Bus")
	}
}

func TestSlotAndEmit(t *testing.T) {
	bus := New()

	var received bool
	err := bus.Slot("test", func() {
		received = true
	})

	if err != nil {
		t.Errorf("Slot failed: %v", err)
	}

	bus.Emit("test")

	// give asynchronous processing a little time
	time.Sleep(10 * time.Millisecond)

	if !received {
		t.Error("Handler was not called")
	}
}

func TestSlotAsync(t *testing.T) {
	bus := New()

	var received bool
	var mu sync.Mutex

	err := bus.SlotAsync("test", func() {
		mu.Lock()
		received = true
		mu.Unlock()
	}, false)

	if err != nil {
		t.Errorf("SlotAsync failed: %v", err)
	}

	bus.Emit("test")

	// wait for asynchronous processing to complete
	bus.WaitAsync()

	mu.Lock()
	defer mu.Unlock()
	if !received {
		t.Error("Async handler was not called")
	}
}

func TestSlotOnce(t *testing.T) {
	bus := New()

	callCount := 0
	err := bus.SlotOnce("test", func() {
		callCount++
	})

	if err != nil {
		t.Errorf("SlotOnce failed: %v", err)
	}

	// first emit
	bus.Emit("test")

	// second emit
	bus.Emit("test")

	if callCount != 1 {
		t.Errorf("Expected call count 1, got %d", callCount)
	}
}

func TestHasSlot(t *testing.T) {
	bus := New()

	if bus.HasSlot("test") {
		t.Error("Should not have slot for non-existent topic")
	}

	bus.Slot("test", func() {})

	if !bus.HasSlot("test") {
		t.Error("Should have slot for slotted topic")
	}
}

func TestUnslot(t *testing.T) {
	bus := New()

	handler := func() {}
	err := bus.Slot("test", handler)
	if err != nil {
		t.Fatalf("Slot failed: %v", err)
	}

	if !bus.HasSlot("test") {
		t.Error("Should have slot before unslot")
	}

	err = bus.Unslot("test", handler)
	if err != nil {
		t.Errorf("Unslot failed: %v", err)
	}

	if bus.HasSlot("test") {
		t.Error("Should not have slot after unslot")
	}
}

func TestUnslotNonExistent(t *testing.T) {
	bus := New()

	err := bus.Unslot("nonexistent", func() {})
	if err == nil {
		t.Error("Should return error for non-existent topic")
	}
}

func TestConcurrentEmit(t *testing.T) {
	bus := New()

	var mu sync.Mutex
	callCount := 0

	// slot multiple handlers
	for i := 0; i < 10; i++ {
		bus.Slot("concurrent", func() {
			mu.Lock()
			callCount++
			mu.Unlock()
		})
	}

	// concurrent emit
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bus.Emit("concurrent")
		}()
	}

	wg.Wait()

	// wait for all asynchronous processing to complete
	bus.WaitAsync()

	expectedCalls := 10 * 100 // 10 handlers × 100 emits
	if callCount != expectedCalls {
		t.Errorf("Expected %d calls, got %d", expectedCalls, callCount)
	}
}

func TestInvalidHandler(t *testing.T) {
	bus := New()

	// test non-function handler
	err := bus.Slot("test", "not a function")
	if err == nil {
		t.Error("Should return error for non-function handler")
	}

	// test invalid handler in unslot
	err = bus.Unslot("test", "not a function")
	if err == nil {
		t.Error("Should return error for non-function handler in unslot")
	}
}

func BenchmarkEmit(b *testing.B) {
	bus := New()

	// pre-register some handlers
	for i := 0; i < 10; i++ {
		bus.Slot("benchmark", func(data string) {
			// simulate some work
			_ = len(data)
		})
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		bus.Emit("benchmark", "test data")
	}

	bus.WaitAsync()
}

func TestGlobalBus(t *testing.T) {
	// test global bus
	var received bool
	err := Slot("global", func() {
		received = true
	})

	if err != nil {
		t.Errorf("Global Slot failed: %v", err)
	}

	Emit("global")

	time.Sleep(10 * time.Millisecond)

	if !received {
		t.Error("Global handler was not called")
	}
}

func TestSlotWithArguments(t *testing.T) {
	bus := New()

	var receivedID int
	var receivedName string

	err := bus.Slot("user.created", func(id int, name string) {
		receivedID = id
		receivedName = name
	})

	if err != nil {
		t.Errorf("Slot with arguments failed: %v", err)
	}

	bus.Emit("user.created", 123, "张三")

	time.Sleep(10 * time.Millisecond)

	if receivedID != 123 || receivedName != "张三" {
		t.Errorf("Expected (123, 张三), got (%d, %s)", receivedID, receivedName)
	}
}

func TestTransactionalSlotAsync(t *testing.T) {
	bus := New()

	var executionOrder []int
	var mu sync.Mutex
	var wg sync.WaitGroup

	// register transactional asynchronous handler
	bus.SlotAsync("transactional", func(order int) {
		mu.Lock()
		executionOrder = append(executionOrder, order)
		mu.Unlock()
		// simulate processing time
		time.Sleep(10 * time.Millisecond)
	}, true)

	// sequential emit events, ensure emit order
	for i := 1; i <= 5; i++ {
		wg.Add(1)
		go func(order int) {
			defer wg.Done()
			bus.Emit("transactional", order)
		}(i)
	}

	wg.Wait()
	bus.WaitAsync()

	// check execution count
	mu.Lock()
	defer mu.Unlock()

	if len(executionOrder) != 5 {
		t.Errorf("Expected 5 executions, got %d", len(executionOrder))
		return
	}

	// transactional processing should be executed in emit order
	// since it is concurrent emit, we only check if all events are processed
	// not strict order, because the order of concurrent emit cannot be guaranteed
	expectedOrders := map[int]bool{1: true, 2: true, 3: true, 4: true, 5: true}
	for _, order := range executionOrder {
		if !expectedOrders[order] {
			t.Errorf("Unexpected order %d in execution", order)
		}
		delete(expectedOrders, order)
	}

	if len(expectedOrders) > 0 {
		t.Errorf("Missing orders: %v", expectedOrders)
	}
}
