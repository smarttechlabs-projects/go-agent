// This file is part of the SmartTechLabs AI Workshop material.
// Contact: ai-lab@smarttechlabs.de — https://www.smarttechlabs.de
// SmartTechLabs is also available for AI projects and consulting.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the LICENSE file in the project root or
// http://www.apache.org/licenses/LICENSE-2.0 for the full text.

package main

import (
	"testing"
	"time"
)

func TestEventBus_EmitAndSubscribe(t *testing.T) {
	bus := NewEventBus()

	ch, unsub := bus.Subscribe()
	defer unsub()

	bus.Emit(Event{Type: EventQueryStart, Data: EventData{Input: "hello"}})

	select {
	case evt := <-ch:
		if evt.Type != EventQueryStart {
			t.Errorf("type = %q, want %q", evt.Type, EventQueryStart)
		}
		if evt.Data.Input != "hello" {
			t.Errorf("input = %q, want 'hello'", evt.Data.Input)
		}
		if evt.Timestamp == 0 {
			t.Error("timestamp should be set")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestEventBus_MultipleSubscribers(t *testing.T) {
	bus := NewEventBus()

	ch1, unsub1 := bus.Subscribe()
	defer unsub1()
	ch2, unsub2 := bus.Subscribe()
	defer unsub2()

	bus.Emit(Event{Type: EventToolCall, Data: EventData{ToolName: "fetch"}})

	for _, ch := range []<-chan Event{ch1, ch2} {
		select {
		case evt := <-ch:
			if evt.Data.ToolName != "fetch" {
				t.Errorf("tool = %q, want 'fetch'", evt.Data.ToolName)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out")
		}
	}
}

func TestEventBus_HistoryReplay(t *testing.T) {
	bus := NewEventBus()

	// Emit before subscribing.
	bus.Emit(Event{Type: EventQueryStart, Data: EventData{Input: "first"}})
	bus.Emit(Event{Type: EventQueryEnd, Data: EventData{FinalAnswer: "done"}})

	// New subscriber should get history.
	ch, unsub := bus.Subscribe()
	defer unsub()

	// Wait briefly for the replay goroutine.
	time.Sleep(50 * time.Millisecond)

	received := 0
	for {
		select {
		case <-ch:
			received++
		case <-time.After(100 * time.Millisecond):
			goto done
		}
	}
done:
	if received != 2 {
		t.Errorf("received %d events, want 2 (history replay)", received)
	}
}

func TestEventBus_Unsubscribe(t *testing.T) {
	bus := NewEventBus()

	ch, unsub := bus.Subscribe()
	unsub()

	// Channel should be closed after unsubscribe.
	_, ok := <-ch
	if ok {
		t.Error("channel should be closed after unsubscribe")
	}
}

func TestEvent_JSON(t *testing.T) {
	evt := Event{
		Type:  EventToolCall,
		Round: 2,
		Data:  EventData{ToolName: "check_port", ToolArgs: `{"port":3000}`},
	}
	js := evt.JSON()
	if js == "" {
		t.Fatal("JSON should not be empty")
	}
	if !contains(js, `"type":"tool_call"`) {
		t.Errorf("JSON missing type: %s", js)
	}
	if !contains(js, `"round":2`) {
		t.Errorf("JSON missing round: %s", js)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
