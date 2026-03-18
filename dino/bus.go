package dino

import (
	"sync"

	eventbus "github.com/xichan96/cortex/pkg/eventbus"
)

type BusEvent struct {
	Type      string      `json:"type"`
	SessionID string      `json:"session_id"`
	Data      interface{} `json:"data,omitempty"`
}

type Bus struct {
	bus eventbus.GenericBus[BusEvent]
}

func NewBus() *Bus {
	return &Bus{
		bus: eventbus.NewGeneric[BusEvent](),
	}
}

func (b *Bus) Publish(eventType string, sessionID string, data interface{}) {
	b.bus.Emit(eventType, BusEvent{
		Type:      eventType,
		SessionID: sessionID,
		Data:      data,
	})
}

func (b *Bus) Subscribe(eventType string, handler func(BusEvent)) {
	b.bus.Slot(eventType, handler)
}

func (b *Bus) SubscribeAsync(eventType string, handler func(BusEvent), transactional bool) {
	b.bus.SlotAsync(eventType, handler, transactional)
}

func (b *Bus) SubscribeOnce(eventType string, handler func(BusEvent)) {
	b.bus.SlotOnce(eventType, handler)
}

func (b *Bus) SubscribeOnceAsync(eventType string, handler func(BusEvent)) {
	b.bus.SlotOnceAsync(eventType, handler)
}

func (b *Bus) Unsubscribe(eventType string, handler func(BusEvent)) bool {
	return b.bus.Unslot(eventType, handler)
}

func (b *Bus) HasSubscribers(eventType string) bool {
	return b.bus.HasSlot(eventType)
}

func (b *Bus) WaitAsync() {
	b.bus.WaitAsync()
}

const (
	EventSessionCreated = "session.created"
	EventSessionUpdated = "session.updated"
	EventSessionDeleted = "session.deleted"
	EventSessionError   = "session.error"

	EventMessageCreated = "message.created"
	EventMessageUpdated = "message.updated"
	EventMessageDeleted = "message.deleted"

	EventToolInvoked = "tool.invoked"
	EventToolResult  = "tool.result"
	EventToolError   = "tool.error"

	EventUsageUpdated = "usage.updated"

	EventApprovalRequired = "approval.required"
	EventApprovalGiven    = "approval.given"
	EventApprovalDenied   = "approval.denied"

	EventPermissionAsked = "permission.asked"
	EventPermissionReply = "permission.reply"
)

func Publish(eventType string, sessionID string, data interface{}) {
	GetGlobalBus().Publish(eventType, sessionID, data)
}

func Subscribe(eventType string, handler func(BusEvent)) {
	GetGlobalBus().Subscribe(eventType, handler)
}

func SubscribeAsync(eventType string, handler func(BusEvent), transactional bool) {
	GetGlobalBus().SubscribeAsync(eventType, handler, transactional)
}

func SubscribeOnce(eventType string, handler func(BusEvent)) {
	GetGlobalBus().SubscribeOnce(eventType, handler)
}

func SubscribeOnceAsync(eventType string, handler func(BusEvent)) {
	GetGlobalBus().SubscribeOnceAsync(eventType, handler)
}

func Unsubscribe(eventType string, handler func(BusEvent)) bool {
	return GetGlobalBus().Unsubscribe(eventType, handler)
}

func HasSubscribers(eventType string) bool {
	return GetGlobalBus().HasSubscribers(eventType)
}

func WaitAsync() {
	GetGlobalBus().WaitAsync()
}

var (
	globalBusOnce sync.Once
	globalBus     *Bus
)

func GetGlobalBus() *Bus {
	globalBusOnce.Do(func() {
		globalBus = NewBus()
	})
	return globalBus
}

func SetGlobalBus(bus *Bus) {
	globalBusOnce.Do(func() {})
	globalBus = bus
}
