package eventbus

import (
	"fmt"
	"reflect"
	"sync"
)

// Emitter defines emission-related bus behavior
type Emitter interface {
	Emit(topic string, args ...interface{})
}

// Slotter defines slot-related bus behavior
type Slotter interface {
	Slot(topic string, fn interface{}) error
	SlotAsync(topic string, fn interface{}, transactional bool) error
	SlotOnce(topic string, fn interface{}) error
	SlotOnceAsync(topic string, fn interface{}) error
	Unslot(topic string, handler interface{}) error
}

// Controller defines bus control behavior (checking handler's presence, synchronization)
type Controller interface {
	HasSlot(topic string) bool
	WaitAsync()
}

// Bus englobes global (slot, emit, control) bus behavior
type Bus interface {
	Controller
	Slotter
	Emitter
}

// GenericBus generic event bus interface
type GenericBus[T any] interface {
	// Slot register event handler
	Slot(topic string, handler func(T))
	// SlotAsync 注册异步事件处理器
	SlotAsync(topic string, handler func(T), transactional bool)
	// SlotOnce register once event handler
	SlotOnce(topic string, handler func(T))
	// SlotOnceAsync register once asynchronous event handler
	SlotOnceAsync(topic string, handler func(T))
	// Emit emit event
	Emit(topic string, data T)
	// Unslot remove event handler
	Unslot(topic string, handler func(T)) bool
	// HasSlot check if there is a handler
	HasSlot(topic string) bool
	// WaitAsync wait for asynchronous processing to complete
	WaitAsync()
}

// StandardBus standard event bus interface (using interface{} type)
type StandardBus interface {
	// Slot register event handler
	Slot(topic string, handler interface{}) error
	// SlotAsync register asynchronous event handler
	SlotAsync(topic string, handler interface{}, transactional bool) error
	// SlotOnce register once event handler
	SlotOnce(topic string, handler interface{}) error
	// SlotOnceAsync register once asynchronous event handler
	SlotOnceAsync(topic string, handler interface{}) error
	// Emit emit event
	Emit(topic string, args ...interface{})
	// Unslot remove event handler
	Unslot(topic string, handler interface{}) error
	// HasSlot check if there is a handler
	HasSlot(topic string) bool
	// WaitAsync wait for asynchronous processing to complete
	WaitAsync()
}

// genericEventHandler generic event handler
type genericEventHandler[T any] struct {
	handler       func(T)
	flagOnce      bool
	async         bool
	transactional bool
	executed      bool
	sync.Mutex
}

// GenericEventBus generic event bus implementation
type GenericEventBus[T any] struct {
	handlers           map[string][]*genericEventHandler[T]
	lock               sync.RWMutex
	wg                 sync.WaitGroup
	transactionalLocks map[string]*sync.Mutex
}

// StandardEventBus standard event bus implementation
type StandardEventBus struct {
	handlers           map[string][]*standardEventHandler
	lock               sync.RWMutex
	wg                 sync.WaitGroup
	transactionalLocks map[string]*sync.Mutex
}

// standardEventHandler standard event handler
type standardEventHandler struct {
	handler       interface{}
	flagOnce      bool
	async         bool
	transactional bool
	executed      bool
	sync.Mutex
}

// Slot register event handler
func (bus *StandardEventBus) Slot(topic string, fn interface{}) error {
	if reflect.ValueOf(fn).Kind() != reflect.Func {
		return fmt.Errorf("handler must be a function")
	}

	bus.lock.Lock()
	defer bus.lock.Unlock()

	if bus.handlers[topic] == nil {
		bus.handlers[topic] = make([]*standardEventHandler, 0, 4)
	}

	bus.handlers[topic] = append(bus.handlers[topic], &standardEventHandler{
		handler:       fn,
		flagOnce:      false,
		async:         false,
		transactional: false,
	})
	return nil
}

// SlotAsync register asynchronous event handler
func (bus *StandardEventBus) SlotAsync(topic string, fn interface{}, transactional bool) error {
	if reflect.ValueOf(fn).Kind() != reflect.Func {
		return fmt.Errorf("handler must be a function")
	}

	bus.lock.Lock()
	defer bus.lock.Unlock()

	if bus.handlers[topic] == nil {
		bus.handlers[topic] = make([]*standardEventHandler, 0, 4)
	}

	bus.handlers[topic] = append(bus.handlers[topic], &standardEventHandler{
		handler:       fn,
		flagOnce:      false,
		async:         true,
		transactional: transactional,
	})
	return nil
}

// SlotOnce register once event handler
func (bus *StandardEventBus) SlotOnce(topic string, fn interface{}) error {
	if reflect.ValueOf(fn).Kind() != reflect.Func {
		return fmt.Errorf("handler must be a function")
	}

	bus.lock.Lock()
	defer bus.lock.Unlock()

	if bus.handlers[topic] == nil {
		bus.handlers[topic] = make([]*standardEventHandler, 0, 4)
	}

	bus.handlers[topic] = append(bus.handlers[topic], &standardEventHandler{
		handler:       fn,
		flagOnce:      true,
		async:         false,
		transactional: false,
	})
	return nil
}

// SlotOnceAsync register once asynchronous event handler
func (bus *StandardEventBus) SlotOnceAsync(topic string, fn interface{}) error {
	if reflect.ValueOf(fn).Kind() != reflect.Func {
		return fmt.Errorf("handler must be a function")
	}

	bus.lock.Lock()
	defer bus.lock.Unlock()

	if bus.handlers[topic] == nil {
		bus.handlers[topic] = make([]*standardEventHandler, 0, 4)
	}

	bus.handlers[topic] = append(bus.handlers[topic], &standardEventHandler{
		handler:       fn,
		flagOnce:      true,
		async:         true,
		transactional: false,
	})
	return nil
}

// Emit emit event
func (bus *StandardEventBus) Emit(topic string, args ...interface{}) {
	bus.lock.RLock()
	handlers, exists := bus.handlers[topic]
	if !exists || len(handlers) == 0 {
		bus.lock.RUnlock()
		return
	}

	// create handler copy
	handlersCopy := make([]*standardEventHandler, len(handlers))
	copy(handlersCopy, handlers)
	bus.lock.RUnlock()

	// check if there is a transactional handler
	hasTransactional := false
	for _, handler := range handlersCopy {
		if handler.async && handler.transactional {
			hasTransactional = true
			break
		}
	}

	// get transactional lock
	var transactionalLock *sync.Mutex
	if hasTransactional {
		bus.lock.Lock()
		if _, exists := bus.transactionalLocks[topic]; !exists {
			bus.transactionalLocks[topic] = &sync.Mutex{}
		}
		transactionalLock = bus.transactionalLocks[topic]
		bus.lock.Unlock()
		transactionalLock.Lock()
	}

	// mark once handlers to remove
	var onceHandlersToRemove []int

	// execute handlers
	for i, handler := range handlersCopy {
		if handler.flagOnce && handler.executed {
			continue
		}

		if handler.flagOnce {
			onceHandlersToRemove = append(onceHandlersToRemove, i)
		}

		if !handler.async {
			bus.executeHandler(handler, args)
		} else {
			bus.wg.Add(1)
			if handler.transactional {
				handler.Lock()
			}
			go bus.executeHandlerAsync(handler, args)
		}
	}

	// release transactional lock
	if hasTransactional {
		go func() {
			bus.wg.Wait()
			transactionalLock.Unlock()
		}()
	}

	// remove once handlers
	if len(onceHandlersToRemove) > 0 {
		bus.removeOnceHandlers(topic, onceHandlersToRemove)
	}
}

// executeHandler execute handler
func (bus *StandardEventBus) executeHandler(handler *standardEventHandler, args []interface{}) {
	if handler.flagOnce {
		handler.Lock()
		if handler.executed {
			handler.Unlock()
			return
		}
		handler.executed = true
		handler.Unlock()
	}

	fnValue := reflect.ValueOf(handler.handler)
	fnType := fnValue.Type()
	numIn := fnType.NumIn()

	callArgs := make([]reflect.Value, numIn)

	if numIn == 0 {
		fnValue.Call(callArgs)
	} else if numIn == 1 && len(args) >= 1 {
		callArgs[0] = reflect.ValueOf(args[0])
		fnValue.Call(callArgs)
	} else if numIn > 1 && len(args) >= numIn {
		for i := 0; i < numIn; i++ {
			callArgs[i] = reflect.ValueOf(args[i])
		}
		fnValue.Call(callArgs)
	} else if len(args) == 1 && numIn > 1 {
		// try to handle single argument as slice
		if slice, ok := args[0].([]interface{}); ok && len(slice) >= numIn {
			for i := 0; i < numIn; i++ {
				callArgs[i] = reflect.ValueOf(slice[i])
			}
			fnValue.Call(callArgs)
		}
	}
}

// executeHandlerAsync execute handler asynchronously
func (bus *StandardEventBus) executeHandlerAsync(handler *standardEventHandler, args []interface{}) {
	defer bus.wg.Done()

	if handler.transactional {
		defer handler.Unlock()
	}

	bus.executeHandler(handler, args)
}

// removeOnceHandlers remove once handlers
func (bus *StandardEventBus) removeOnceHandlers(topic string, indices []int) {
	bus.lock.Lock()
	defer bus.lock.Unlock()

	handlers, exists := bus.handlers[topic]
	if !exists {
		return
	}

	// 从后往前移除，避免索引变化
	for i := len(indices) - 1; i >= 0; i-- {
		index := indices[i]
		if index < len(handlers) {
			bus.handlers[topic] = append(handlers[:index], handlers[index+1:]...)
		}
	}
}

// Unslot 移除事件处理器
func (bus *StandardEventBus) Unslot(topic string, fn interface{}) error {
	bus.lock.Lock()
	defer bus.lock.Unlock()

	handlers, exists := bus.handlers[topic]
	if !exists {
		return fmt.Errorf("topic not found")
	}

	// find handler index
	index := -1
	for i, h := range handlers {
		// use reflection to compare function pointers
		if reflect.ValueOf(h.handler).Pointer() == reflect.ValueOf(fn).Pointer() {
			index = i
			break
		}
	}

	if index == -1 {
		return fmt.Errorf("handler not found")
	}

	// remove handler
	bus.handlers[topic] = append(handlers[:index], handlers[index+1:]...)
	return nil
}

// HasSlot check if there is a handler
func (bus *StandardEventBus) HasSlot(topic string) bool {
	bus.lock.RLock()
	defer bus.lock.RUnlock()

	handlers, exists := bus.handlers[topic]
	return exists && len(handlers) > 0
}

// WaitAsync wait for asynchronous processing to complete
func (bus *StandardEventBus) WaitAsync() {
	bus.wg.Wait()
}

// New create a new event bus instance
func New() Bus {
	return &StandardEventBus{
		handlers:           make(map[string][]*standardEventHandler),
		lock:               sync.RWMutex{},
		wg:                 sync.WaitGroup{},
		transactionalLocks: make(map[string]*sync.Mutex),
	}
}

// NewGeneric create generic event bus instance
func NewGeneric[T any]() GenericBus[T] {
	return &GenericEventBus[T]{
		handlers:           make(map[string][]*genericEventHandler[T]),
		lock:               sync.RWMutex{},
		wg:                 sync.WaitGroup{},
		transactionalLocks: make(map[string]*sync.Mutex),
	}
}

// Slot register event handler
func (bus *GenericEventBus[T]) Slot(topic string, handler func(T)) {
	bus.lock.Lock()
	defer bus.lock.Unlock()

	if bus.handlers[topic] == nil {
		bus.handlers[topic] = make([]*genericEventHandler[T], 0, 4)
	}

	bus.handlers[topic] = append(bus.handlers[topic], &genericEventHandler[T]{
		handler:       handler,
		flagOnce:      false,
		async:         false,
		transactional: false,
	})
}

// SlotAsync register asynchronous event handler
func (bus *GenericEventBus[T]) SlotAsync(topic string, handler func(T), transactional bool) {
	bus.lock.Lock()
	defer bus.lock.Unlock()

	if bus.handlers[topic] == nil {
		bus.handlers[topic] = make([]*genericEventHandler[T], 0, 4)
	}

	bus.handlers[topic] = append(bus.handlers[topic], &genericEventHandler[T]{
		handler:       handler,
		flagOnce:      false,
		async:         true,
		transactional: transactional,
	})
}

// SlotOnce register once event handler
func (bus *GenericEventBus[T]) SlotOnce(topic string, handler func(T)) {
	bus.lock.Lock()
	defer bus.lock.Unlock()

	if bus.handlers[topic] == nil {
		bus.handlers[topic] = make([]*genericEventHandler[T], 0, 4)
	}

	bus.handlers[topic] = append(bus.handlers[topic], &genericEventHandler[T]{
		handler:       handler,
		flagOnce:      true,
		async:         false,
		transactional: false,
	})
}

// SlotOnceAsync register once asynchronous event handler
func (bus *GenericEventBus[T]) SlotOnceAsync(topic string, handler func(T)) {
	bus.lock.Lock()
	defer bus.lock.Unlock()

	if bus.handlers[topic] == nil {
		bus.handlers[topic] = make([]*genericEventHandler[T], 0, 4)
	}

	bus.handlers[topic] = append(bus.handlers[topic], &genericEventHandler[T]{
		handler:       handler,
		flagOnce:      true,
		async:         true,
		transactional: false,
	})
}

// Emit emit event
func (bus *GenericEventBus[T]) Emit(topic string, data T) {
	bus.lock.RLock()
	handlers, exists := bus.handlers[topic]
	if !exists || len(handlers) == 0 {
		bus.lock.RUnlock()
		return
	}

	// create handler copy
	handlersCopy := make([]*genericEventHandler[T], len(handlers))
	copy(handlersCopy, handlers)
	bus.lock.RUnlock()

	// check if there is a transactional handler
	hasTransactional := false
	for _, handler := range handlersCopy {
		if handler.async && handler.transactional {
			hasTransactional = true
			break
		}
	}

	// get transactional lock
	var transactionalLock *sync.Mutex
	if hasTransactional {
		bus.lock.Lock()
		if _, exists := bus.transactionalLocks[topic]; !exists {
			bus.transactionalLocks[topic] = &sync.Mutex{}
		}
		transactionalLock = bus.transactionalLocks[topic]
		bus.lock.Unlock()
		transactionalLock.Lock()
	}

	// mark once handlers to remove
	var onceHandlersToRemove []int

	// execute handlers
	for i, handler := range handlersCopy {
		if handler.flagOnce && handler.executed {
			continue
		}

		if handler.flagOnce {
			onceHandlersToRemove = append(onceHandlersToRemove, i)
		}

		if !handler.async {
			bus.executeHandler(handler, data)
		} else {
			bus.wg.Add(1)
			if handler.transactional {
				handler.Lock()
			}
			go bus.executeHandlerAsync(handler, data)
		}
	}

	// release transactional lock
	if hasTransactional {
		go func() {
			bus.wg.Wait()
			transactionalLock.Unlock()
		}()
	}

	// remove once handlers
	if len(onceHandlersToRemove) > 0 {
		bus.removeOnceHandlers(topic, onceHandlersToRemove)
	}
}

// executeHandler execute handler
func (bus *GenericEventBus[T]) executeHandler(handler *genericEventHandler[T], data T) {
	if handler.flagOnce {
		handler.Lock()
		if handler.executed {
			handler.Unlock()
			return
		}
		handler.executed = true
		handler.Unlock()
	}

	handler.handler(data)
}

// executeHandlerAsync execute handler asynchronously
func (bus *GenericEventBus[T]) executeHandlerAsync(handler *genericEventHandler[T], data T) {
	defer bus.wg.Done()
	if handler.transactional {
		defer handler.Unlock()
	}

	bus.executeHandler(handler, data)
}

// removeOnceHandlers remove once handlers
func (bus *GenericEventBus[T]) removeOnceHandlers(topic string, indices []int) {
	bus.lock.Lock()
	defer bus.lock.Unlock()

	handlers, exists := bus.handlers[topic]
	if !exists {
		return
	}

	// create new slice, exclude handlers to remove
	newHandlers := make([]*genericEventHandler[T], 0, len(handlers)-len(indices))
	for i, handler := range handlers {
		shouldRemove := false
		for _, idx := range indices {
			if i == idx {
				shouldRemove = true
				break
			}
		}
		if !shouldRemove {
			newHandlers = append(newHandlers, handler)
		}
	}

	bus.handlers[topic] = newHandlers
}

// Unslot remove event handler
func (bus *GenericEventBus[T]) Unslot(topic string, handler func(T)) bool {
	bus.lock.Lock()
	defer bus.lock.Unlock()

	handlers, exists := bus.handlers[topic]
	if !exists {
		return false
	}

	// find handler index
	index := -1
	for i, h := range handlers {
		// use reflection to compare function pointers
		if reflect.ValueOf(h.handler).Pointer() == reflect.ValueOf(handler).Pointer() {
			index = i
			break
		}
	}

	if index == -1 {
		return false
	}

	// remove handler
	bus.handlers[topic] = append(handlers[:index], handlers[index+1:]...)
	return true
}

// HasSlot check if there is a handler
func (bus *GenericEventBus[T]) HasSlot(topic string) bool {
	bus.lock.RLock()
	defer bus.lock.RUnlock()

	handlers, exists := bus.handlers[topic]
	return exists && len(handlers) > 0
}

// WaitAsync wait for asynchronous processing to complete
func (bus *GenericEventBus[T]) WaitAsync() {
	bus.wg.Wait()
}

// isSameHandler compare two functions whether they are the same
func isSameHandler[T any](a, b func(T)) bool {
	// due to Go language limitations, it is not possible to directly compare function pointers
	// here we use the alternative of type assertion and reflection
	// in actual use, users should save handler references for Unslot
	return false // always return false, users should manage handler references themselves
}

// global event bus instance
var (
	// GlobalBus global event bus
	GlobalBus = New()
)

// convenient function - use global bus

// Slot use global bus to register event handler
func Slot(topic string, fn interface{}) error {
	return GlobalBus.Slot(topic, fn)
}

// SlotAsync use global bus to register asynchronous event handler
func SlotAsync(topic string, fn interface{}, transactional bool) error {
	return GlobalBus.SlotAsync(topic, fn, transactional)
}

// SlotOnce use global bus to register once event handler
func SlotOnce(topic string, fn interface{}) error {
	return GlobalBus.SlotOnce(topic, fn)
}

// SlotOnceAsync use global bus to register once asynchronous event handler
func SlotOnceAsync(topic string, fn interface{}) error {
	return GlobalBus.SlotOnceAsync(topic, fn)
}

// Emit use global bus to emit event
func Emit(topic string, args ...interface{}) {
	GlobalBus.Emit(topic, args...)
}

// Unslot use global bus to remove event handler
func Unslot(topic string, handler interface{}) error {
	return GlobalBus.Unslot(topic, handler)
}

// HasSlot use global bus to check if there is a handler
func HasSlot(topic string) bool {
	return GlobalBus.HasSlot(topic)
}

// WaitAsync use global bus to wait for asynchronous processing to complete
func WaitAsync() {
	GlobalBus.WaitAsync()
}
