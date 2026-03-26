package event

import fbprotocol "github.com/facebook/time/ptp/protocol"

// EventHandlerForTests wraps EventHandler with test-only helpers
// that access unexported fields. This keeps test utilities out of
// production code.
type EventHandlerForTests struct {
	*EventHandler
}

// NewEventHandlerForTests creates an EventHandlerForTests wrapping
// a minimal EventHandler for socket logic tests.
func NewEventHandlerForTests(socketPath string) *EventHandlerForTests {
	return &EventHandlerForTests{
		EventHandler: newTestEventHandler(socketPath),
	}
}

// SetClockClass populates the clock sync state for the given config
// so that EmitClockClass has data to emit.
func (t *EventHandlerForTests) SetClockClass(cfgName string, clockClass uint8) {
	t.Lock()
	defer t.Unlock()
	t.clkSyncState[cfgName] = &clockSyncState{
		clockClass: fbprotocol.ClockClass(clockClass),
	}
}
