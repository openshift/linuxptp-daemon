package ublox

import (
	"strconv"
	"strings"
	"time"
)

// Parser converts ubxtool's line-oriented output into typed messages. A
// parser is intentionally independent of a process or a channel so it can be
// used by both the long-running receiver and command invocations.
type parser struct {
	currentType      MessageType
	currentHeader    string
	currentTimestamp time.Time
	pendingTimestamp time.Time
	lines            []string
}

// newParser returns a parser for one ubxtool output stream.
func newParser() *parser {
	return &parser{}
}

// feed consumes one output line and returns any message completed by it. A
// message is complete when the next UBX header is encountered.
func (p *parser) feed(line string) []Message {
	typ, ok := messageTypeFromLine(line)
	if ok {
		messages := p.finish()
		p.currentType = typ
		p.currentHeader = strings.TrimSpace(line)
		p.currentTimestamp = p.pendingTimestamp
		p.pendingTimestamp = time.Time{}
		p.lines = nil
		return messages
	}

	// With -t, ubxtool writes the timestamp on its own line immediately
	// before each message header. Seeing it also terminates the preceding
	// message, whose body has no explicit end marker.
	if timestamp, timestampOK := timestampOnlyLine(line); timestampOK {
		messages := p.finish()
		p.pendingTimestamp = timestamp
		return messages
	}

	if p.currentType != "" {
		p.lines = append(p.lines, line)
		if p.messageComplete() {
			return p.finish()
		}
	}
	return nil
}

// messageComplete reports whether the current message has a known terminal body.
func (p *parser) messageComplete() bool {
	switch p.currentType {
	case AckAckType, AckNakType:
		_, hasClassID := fieldValue(p.lines, "clsID")
		_, hasMessageID := fieldValue(p.lines, "msgID")
		return hasClassID && hasMessageID
	default:
		return false
	}
}

// flush completes the final message in a stream that has ended without a
// following header.
func (p *parser) flush() []Message {
	return p.finish()
}

// finish decodes and clears the current message.
func (p *parser) finish() []Message {
	if p.currentType == "" {
		return nil
	}

	raw := make([]string, 0, len(p.lines)+1)
	raw = append(raw, p.currentHeader)
	raw = append(raw, p.lines...)
	message := Message{
		Type:      p.currentType,
		Timestamp: p.currentTimestamp,
		Received:  time.Now(),
		Payload:   decodePayload(p.currentType, p.lines),
		Raw:       raw,
	}

	p.currentType = ""
	p.currentHeader = ""
	p.currentTimestamp = time.Time{}
	p.lines = nil
	return []Message{message}
}

// timestampOnlyLine extracts the Unix timestamp emitted on its own line by
// ubxtool -t, immediately before a message header.
func timestampOnlyLine(line string) (time.Time, bool) {
	fields := strings.Fields(line)
	if len(fields) != 1 {
		return time.Time{}, false
	}
	return parseUnixTimestamp(fields[0])
}

func parseUnixTimestamp(value string) (time.Time, bool) {
	parts := strings.SplitN(value, ".", 2)
	seconds, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || seconds < 0 {
		return time.Time{}, false
	}

	nanoseconds := int64(0)
	if len(parts) == 2 {
		fraction := parts[1]
		if fraction == "" {
			return time.Time{}, false
		}
		if len(fraction) > 9 {
			fraction = fraction[:9]
		}
		fraction += strings.Repeat("0", 9-len(fraction))
		nanoseconds, err = strconv.ParseInt(fraction, 10, 64)
		if err != nil {
			return time.Time{}, false
		}
	}
	return time.Unix(seconds, nanoseconds), true
}

// messageTypeFromLine extracts a UBX message type from a rendered line.
func messageTypeFromLine(line string) (MessageType, bool) {
	for _, field := range strings.Fields(line) {
		if strings.HasPrefix(field, "UBX-") {
			return MessageType(strings.TrimSuffix(field, ":")), true
		}
	}
	return "", false
}

// decodePayload converts rendered message fields into a typed payload.
func decodePayload(typ MessageType, lines []string) Payload {
	switch typ {
	case NavClockType:
		tAcc, hasTAcc := fieldUint64Value(lines, "tAcc")
		// Keep unavailable accuracy outside the daemon's valid offset range.
		offset := unknownOffset
		if hasTAcc {
			offset = int64(tAcc)
		}
		return NavClock{
			ITOW:   fieldUint32(lines, "iTOW"),
			ClkB:   fieldInt64(lines, "clkB", 0),
			ClkD:   fieldInt64(lines, "clkD", 0),
			TAcc:   tAcc,
			FAcc:   fieldUint64(lines, "fAcc"),
			Offset: offset,
		}
	case NavStatusType:
		return NavStatus{
			ITOW:    fieldUint32(lines, "iTOW"),
			GPSFix:  fieldInt64(lines, "gpsFix", -1),
			Flags:   fieldUint8(lines, "flags"),
			FixStat: fieldUint8(lines, "fixStat"),
			Flags2:  fieldUint8(lines, "flags2"),
			TTFF:    fieldUint32(lines, "ttff"),
			MSSS:    fieldUint32(lines, "msss"),
		}
	case NavTimeLsType:
		return *ExtractLeapSec(lines)
	case NavSvinType:
		return SurveyIn{
			Duration:     fieldUint64(lines, "dur"),
			MeanX:        fieldInt64(lines, "meanX", 0),
			MeanY:        fieldInt64(lines, "meanY", 0),
			MeanZ:        fieldInt64(lines, "meanZ", 0),
			MeanV:        fieldInt64(lines, "meanV", 0),
			MeanAccuracy: fieldUint64(lines, "meanAcc"),
			Observations: fieldUint64(lines, "obs"),
			Valid:        fieldBool(lines, "valid"),
			Active:       fieldBool(lines, "active"),
		}
	case AckAckType:
		return AckAck{
			ClassID:   fieldUint8(lines, "clsID"),
			MessageID: fieldUint8(lines, "msgID"),
		}
	case AckNakType:
		return AckNak{
			ClassID:   fieldUint8(lines, "clsID"),
			MessageID: fieldUint8(lines, "msgID"),
		}
	default:
		return RawMessage{Type: typ, Lines: append([]string(nil), lines...)}
	}
}

// fieldValue returns the token following a named rendered field.
func fieldValue(lines []string, name string) (string, bool) {
	for _, line := range lines {
		fields := strings.Fields(line)
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == name {
				return fields[i+1], true
			}
		}
	}
	return "", false
}

// fieldInt64 parses a signed field or returns fallback when unavailable.
func fieldInt64(lines []string, name string, fallback int64) int64 {
	value, ok := fieldValue(lines, name)
	if !ok {
		return fallback
	}
	parsed, err := parseInt(value, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

// fieldUint8 parses an unsigned eight-bit field or returns zero.
func fieldUint8(lines []string, name string) uint8 {
	value, ok := fieldValue(lines, name)
	if !ok {
		return 0
	}
	parsed, err := parseUint(value, 8)
	if err != nil {
		return 0
	}
	return uint8(parsed)
}

// fieldUint32 parses an unsigned 32-bit field or returns zero.
func fieldUint32(lines []string, name string) uint32 {
	value, ok := fieldValue(lines, name)
	if !ok {
		return 0
	}
	parsed, err := parseUint(value, 32)
	if err != nil {
		return 0
	}
	return uint32(parsed)
}

// fieldUint64 parses an unsigned 64-bit field or returns zero.
func fieldUint64(lines []string, name string) uint64 {
	value, _ := fieldUint64Value(lines, name)
	return value
}

// fieldUint64Value parses an unsigned 64-bit field and reports validity.
func fieldUint64Value(lines []string, name string) (uint64, bool) {
	value, ok := fieldValue(lines, name)
	if !ok {
		return 0, false
	}
	parsed, err := parseUint(value, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

// fieldBool parses a boolean field from common ubxtool representations.
func fieldBool(lines []string, name string) bool {
	value, ok := fieldValue(lines, name)
	if !ok {
		return false
	}
	return value == "1" || strings.EqualFold(value, "true")
}

// parseInt parses decimal and hexadecimal signed ubxtool values.
func parseInt(value string, bitSize int) (int64, error) {
	value = cleanNumber(value)
	return strconv.ParseInt(normalizeNumber(value), numberBase(value), bitSize)
}

// parseUint parses decimal and hexadecimal unsigned ubxtool values.
func parseUint(value string, bitSize int) (uint64, error) {
	value = cleanNumber(value)
	return strconv.ParseUint(normalizeNumber(value), numberBase(value), bitSize)
}

// cleanNumber removes punctuation trailing a rendered numeric value.
func cleanNumber(value string) string {
	return strings.TrimRight(value, ",")
}

// normalizeNumber converts ubxtool's x-prefixed values to Go syntax.
func normalizeNumber(value string) string {
	if strings.HasPrefix(value, "x") || strings.HasPrefix(value, "X") {
		return "0" + value
	}
	if strings.HasPrefix(value, "-x") || strings.HasPrefix(value, "-X") {
		return "-0" + value[1:]
	}
	return value
}

// numberBase selects the numeric base used to parse a rendered value.
func numberBase(value string) int {
	value = normalizeNumber(value)
	value = strings.TrimPrefix(value, "-")
	if strings.HasPrefix(value, "0x") || strings.HasPrefix(value, "0X") {
		return 0
	}
	return 10
}
