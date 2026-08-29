package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino/mem"
)

const defaultErrFlushWait = 120 * time.Millisecond

type TurnObservation struct {
	AssistantText   string
	Usage           *Usage
	HadError        bool
	ErrorMessage    string
	EngineStopCause string
}

type TurnCanceledError struct {
	Err error
}

func (e TurnCanceledError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "turn canceled"
}

func (e TurnCanceledError) Unwrap() error { return e.Err }

type TurnSessionClosedError struct {
	Err error
}

func (e TurnSessionClosedError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "session closed"
}

func (e TurnSessionClosedError) Unwrap() error { return e.Err }

func ObserveOneUserTurn(ctx context.Context, s *Session, in types.AgentInput) (*TurnObservation, bool, error) {
	if s == nil {
		return nil, false, errors.New("nil session")
	}
	obs := &TurnObservation{}
	var mu sync.Mutex
	sawDone := false
	userEchoLeft := 1
	finish := make(chan struct{})
	var finishOnce sync.Once
	closeFinish := func() {
		finishOnce.Do(func() { close(finish) })
	}

	var debounce *time.Timer
	scheduleFlush := func() {
		if debounce != nil {
			debounce.Stop()
		}
		debounce = time.AfterFunc(defaultErrFlushWait, func() {
			mu.Lock()
			defer mu.Unlock()
			if !sawDone {
				closeFinish()
			}
		})
	}

	obsID := s.Subscribe(ObserverFunc(func(ev *Event) {
		if ev == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()

		switch ev.Type {
		case EventTypeMessage:
			// S4/B2（评审 O-3）：唤醒注入 turn 的幽灵消息不累计进 AssistantText。
			if ev.Source == EventSourceSubagent {
				break
			}
			if userEchoLeft > 0 {
				userEchoLeft--
				break
			}
			obs.AssistantText += ev.Content
		case EventTypeTokenUsage:
			if ev.Usage != nil {
				u := *ev.Usage
				obs.Usage = &u
			}
		case EventTypeError:
			obs.HadError = true
			if ev.Error != "" {
				obs.ErrorMessage = ev.Error
			} else {
				obs.ErrorMessage = ev.Content
			}
			if ev.StopCause != "" {
				obs.EngineStopCause = ev.StopCause
			}
			scheduleFlush()
		case EventTypeDone:
			if debounce != nil {
				debounce.Stop()
				debounce = nil
			}
			if ev.StopCause != "" {
				obs.EngineStopCause = ev.StopCause
			}
			sawDone = true
			closeFinish()
		}
	}))
	defer s.Unsubscribe(obsID)

	select {
	case s.Input() <- in:
	case <-ctx.Done():
		return nil, false, TurnCanceledError{Err: ctx.Err()}
	}

	select {
	case <-finish:
	case <-ctx.Done():
		s.CancelCurrentTurn()
		return nil, false, TurnCanceledError{Err: ctx.Err()}
	case <-s.Context().Done():
		return nil, false, TurnSessionClosedError{Err: s.Context().Err()}
	}

	mu.Lock()
	assistantText := obs.AssistantText
	mu.Unlock()

	// 引用反馈（B3）：turn 结束后，若模型最终输出包含本次 turn 内 search_knowledge
	// 返回条目的内容片段，才计数为一次实际使用。
	if strings.TrimSpace(assistantText) != "" {
		mem.ObserveAssistantFeedback(ctx, s.id, assistantText)
	}

	mu.Lock()
	defer mu.Unlock()
	return obs, sawDone, nil
}
