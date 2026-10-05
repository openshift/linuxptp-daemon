package ublox

import "time"

// MessageType identifies a UBX message as it is rendered by ubxtool.
// Keeping the rendered name here also allows callers to subscribe to message
// types that the package does not decode yet.
type MessageType string

const (
	// NavClockType identifies UBX-NAV-CLOCK messages.
	NavClockType MessageType = "UBX-NAV-CLOCK"
	// NavStatusType identifies UBX-NAV-STATUS messages.
	NavStatusType MessageType = "UBX-NAV-STATUS"
	// NavTimeLsType identifies UBX-NAV-TIMELS messages.
	NavTimeLsType MessageType = "UBX-NAV-TIMELS"
	// NavSvinType identifies UBX-TIM-SVIN messages.
	NavSvinType MessageType = "UBX-TIM-SVIN"
	// AckAckType identifies positive UBX command acknowledgements.
	AckAckType MessageType = "UBX-ACK-ACK"
	// AckNakType identifies negative UBX command acknowledgements.
	AckNakType MessageType = "UBX-ACK-NAK"
	// MonHWType identifies UBX-MON-HW poll responses.
	MonHWType MessageType = "UBX-MON-HW"
)

// Payload is the decoded body of a UBX message. The unexported method keeps
// the set of payload implementations owned by this package while allowing
// consumers to type-switch on the exported concrete types.
type Payload interface {
	messageType() MessageType
}

// Message is the common envelope delivered by Receiver subscriptions.
type Message struct {
	Type MessageType
	// Timestamp is the timestamp emitted by ubxtool for this message. It is
	// zero when the stream entry does not contain a parseable timestamp.
	Timestamp time.Time
	// Received is when the daemon completed parsing the message.
	Received time.Time
	Payload  Payload
	Raw      []string
}

// MessageFilter selects messages for a subscription.
type MessageFilter func(Message) bool

// NavClock contains all fields rendered by ubxtool for UBX-NAV-CLOCK.
type NavClock struct {
	ITOW uint32
	ClkB int64
	ClkD int64
	TAcc uint64
	FAcc uint64

	// Offset is retained as the daemon-facing alias for TAcc.
	Offset int64
}

// messageType identifies the UBX type represented by a NavClock payload.
func (NavClock) messageType() MessageType { return NavClockType }

// NavStatus contains all fields rendered by ubxtool for UBX-NAV-STATUS.
type NavStatus struct {
	ITOW    uint32
	GPSFix  int64
	Flags   uint8
	FixStat uint8
	Flags2  uint8
	TTFF    uint32
	MSSS    uint32
}

// messageType identifies the UBX type represented by a NavStatus payload.
func (NavStatus) messageType() MessageType { return NavStatusType }

// SurveyIn contains the fields rendered by ubxtool for UBX-TIM-SVIN.
type SurveyIn struct {
	Duration     uint64
	MeanX        int64
	MeanY        int64
	MeanZ        int64
	MeanV        int64
	MeanAccuracy uint64
	Observations uint64
	Valid        bool
	Active       bool
}

// messageType identifies the UBX type represented by a SurveyIn payload.
func (SurveyIn) messageType() MessageType { return NavSvinType }

// AckAck contains the class and message IDs of an acknowledged command.
type AckAck struct {
	ClassID   uint8
	MessageID uint8
}

// messageType identifies the UBX type represented by an AckAck payload.
func (AckAck) messageType() MessageType { return AckAckType }

// AckNak contains the class and message IDs of a rejected command.
type AckNak struct {
	ClassID   uint8
	MessageID uint8
}

// messageType identifies the UBX type represented by an AckNak payload.
func (AckNak) messageType() MessageType { return AckNakType }

// messageType identifies the UBX type represented by a TimeLs payload.
func (TimeLs) messageType() MessageType { return NavTimeLsType }

// RawMessage is delivered for a recognized UBX message that does not yet
// have a typed decoder.
type RawMessage struct {
	Type  MessageType
	Lines []string
}

// messageType returns the type retained by an undecoded message.
func (m RawMessage) messageType() MessageType { return m.Type }

// Subscription receives every message matching its registration. Messages is
// receive-only so the receiver remains the owner of the channel lifecycle.
type Subscription struct {
	Messages <-chan Message
	cancel   func()
}

// Cancel removes the subscription and closes Messages. It is safe to call
// more than once.
func (s *Subscription) Cancel() {
	if s != nil && s.cancel != nil {
		s.cancel()
	}
}
