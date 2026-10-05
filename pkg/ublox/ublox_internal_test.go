package ublox

import (
	"bufio"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_BatchMsgoutAllBusses(t *testing.T) {
	msgs := []string{navEnableMsg[0]} // single message
	cmd := batchMsgoutAllBusses("UBX_NAV", msgs, 1)
	// Should produce one -z pair per bus type
	assert.Equal(t, len(ublxBusTypes)*2, len(cmd.Args))
	for _, bus := range ublxBusTypes {
		expected := fmt.Sprintf("CFG-MSGOUT-UBX_NAV_%s_%s,1", msgs[0], bus)
		assert.Contains(t, cmd.Args, expected)
	}
}

func Test_BatchMsgoutAllBusses_MultipleMessages(t *testing.T) {
	allNavMsgs := append(navEnableMsg, navSlowEnableMsg...)
	cmd := batchMsgoutAllBusses("UBX_NAV", allNavMsgs, 1)
	// N messages × 5 bus types × 2 args each (-z + value)
	assert.Equal(t, len(allNavMsgs)*len(ublxBusTypes)*2, len(cmd.Args))
	for _, msg := range allNavMsgs {
		for _, bus := range ublxBusTypes {
			expected := fmt.Sprintf("CFG-MSGOUT-UBX_NAV_%s_%s,1", msg, bus)
			assert.Contains(t, cmd.Args, expected)
		}
	}
}

func Test_BatchDisableNmeaMsgs(t *testing.T) {
	cmd := batchDisableNmeaMsgs([]string{"FOO", "BAR"})
	for _, msg := range []string{"FOO", "BAR"} {
		for _, bus := range ublxBusTypes {
			expected := fmt.Sprintf("CFG-MSGOUT-NMEA_ID_%s_%s,0", msg, bus)
			assert.Contains(t, cmd.Args, expected)
		}
	}
}

func Test_BatchEnableNavMsgs(t *testing.T) {
	cmd := batchEnableNavMsgs(navEnableMsg)
	for _, msg := range navEnableMsg {
		for _, bus := range ublxBusTypes {
			expected := fmt.Sprintf("CFG-MSGOUT-UBX_NAV_%s_%s,1", msg, bus)
			assert.Contains(t, cmd.Args, expected)
		}
	}
}

func Test_BatchEnableNavMsgsAtRate(t *testing.T) {
	cmd := batchEnableNavMsgsAtRate(navSlowEnableMsg, timeLSRateSeconds)
	for _, msg := range navSlowEnableMsg {
		for _, bus := range ublxBusTypes {
			expected := fmt.Sprintf("CFG-MSGOUT-UBX_NAV_%s_%s,%d", msg, bus, timeLSRateSeconds)
			assert.Contains(t, cmd.Args, expected)
		}
	}
}

func TestParserParsesCapturedUbxtoolStream(t *testing.T) {
	file, err := os.Open("testdata/ubxtool.log")
	require.NoError(t, err)
	defer file.Close()

	parser := newParser()
	var messages []Message
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		messages = append(messages, parser.feed(scanner.Text()+"\n")...)
	}
	require.NoError(t, scanner.Err())
	messages = append(messages, parser.flush()...)

	require.Len(t, messages, 32)
	firstTimestamp := time.Unix(1789719553, 39400000)
	assert.Equal(t, firstTimestamp, messages[0].Timestamp)
	assert.Equal(t, NavStatusType, messages[0].Type)
	status, ok := messages[0].Payload.(NavStatus)
	require.True(t, ok)
	assert.Equal(t, int64(5), status.GPSFix)

	last := messages[len(messages)-1]
	assert.Equal(t, time.Unix(1789719562, 36600000), last.Timestamp)
	assert.Equal(t, MessageType("UBX-TIM-SVIN"), last.Type)
}

func TestParserParsesCapturedUbxtoolStreamWithoutTimestamps(t *testing.T) {
	file, err := os.Open("testdata/ubxtool_no_timestamp.log")
	require.NoError(t, err)
	defer file.Close()

	parser := newParser()
	var messages []Message
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		messages = append(messages, parser.feed(scanner.Text()+"\n")...)
	}
	require.NoError(t, scanner.Err())
	messages = append(messages, parser.flush()...)

	require.Len(t, messages, 33)
	for _, message := range messages {
		assert.True(t, message.Timestamp.IsZero(), "unexpected timestamp for %s", message.Type)
	}
	assert.Equal(t, NavStatusType, messages[0].Type)
	status, ok := messages[0].Payload.(NavStatus)
	require.True(t, ok)
	assert.Equal(t, int64(5), status.GPSFix)
	assert.Equal(t, uint32(462982000), status.ITOW)
}

func TestParserDecodesTypedMessages(t *testing.T) {
	parser := newParser()
	var messages []Message
	for _, line := range []string{
		"UBX-NAV-STATUS:\n",
		"  iTOW 125060000 gpsFix 3 flags 0xdd fixStat 0x0 flags2 0x8\n",
		"  ttff 565, msss 807626316\n",
		"UBX-NAV-DOP:\n",
		"  iTOW 125060000 gDOP 141 pDOP 122 tDOP 71 vDOP 106\n",
		"UBX-NAV-CLOCK:\n",
		"  iTOW 125060000 clkB 277987 clkD 319 tAcc 9 fAcc 236\n",
	} {
		messages = append(messages, parser.feed(line)...)
	}
	messages = append(messages, parser.flush()...)

	if assert.Len(t, messages, 3) {
		status, ok := messages[0].Payload.(NavStatus)
		require.True(t, ok)
		assert.Equal(t, uint32(125060000), status.ITOW)
		assert.Equal(t, int64(3), status.GPSFix)
		assert.Equal(t, uint8(0xdd), status.Flags)
		assert.Equal(t, uint8(0x0), status.FixStat)
		assert.Equal(t, uint8(0x8), status.Flags2)
		assert.Equal(t, uint32(565), status.TTFF)
		assert.Equal(t, uint32(807626316), status.MSSS)

		_, ok = messages[1].Payload.(RawMessage)
		assert.True(t, ok)

		clock, ok := messages[2].Payload.(NavClock)
		require.True(t, ok)
		assert.Equal(t, uint32(125060000), clock.ITOW)
		assert.Equal(t, int64(277987), clock.ClkB)
		assert.Equal(t, int64(319), clock.ClkD)
		assert.Equal(t, uint64(9), clock.TAcc)
		assert.Equal(t, uint64(236), clock.FAcc)
		assert.Equal(t, int64(9), clock.Offset)
	}
}

func TestParserDecodesRealWorldTimeLs(t *testing.T) {
	parser := newParser()
	for _, line := range []string{
		"UBX-NAV-TIMELS:\n",
		"  iTOW 127297000 version 0 reserved2 0 0 0 srcOfCurrLs 4\n",
		"  currLs 18 srcOfLsChange 4 lsChange 0 timeToLsEvent -306156079\n",
		"  dateOfLsGpsWn 1929 dateOfLsGpsDn 7 reserved2 0 0 0\n",
		"  valid x3\n",
	} {
		parser.feed(line)
	}

	messages := parser.flush()
	require.Len(t, messages, 1)
	leapSeconds, ok := messages[0].Payload.(TimeLs)
	require.True(t, ok)
	assert.Equal(t, uint8(4), leapSeconds.SrcOfCurrLs)
	assert.Equal(t, int8(18), leapSeconds.CurrLs)
	assert.Equal(t, uint8(4), leapSeconds.SrcOfLsChange)
	assert.Equal(t, int8(0), leapSeconds.LsChange)
	assert.Equal(t, -306156079, leapSeconds.TimeToLsEvent)
	assert.Equal(t, uint(1929), leapSeconds.DateOfLsGpsWn)
	assert.Equal(t, uint8(7), leapSeconds.DateOfLsGpsDn)
	assert.Equal(t, uint8(3), leapSeconds.Valid)
}

func TestParserPublishesFinalAckWhenBodyEnds(t *testing.T) {
	parser := newParser()
	messages := parser.feed("UBX-ACK-ACK:\n")
	assert.Empty(t, messages)

	messages = parser.feed("  clsID 0x06 msgID 0x01\n")
	require.Len(t, messages, 1)
	assert.Equal(t, AckAckType, messages[0].Type)
}

func TestParserDecodesAckMessages(t *testing.T) {
	parser := newParser()
	var messages []Message
	for _, line := range []string{
		"UBX-ACK-ACK:\n",
		"  clsID 0x06 msgID 0x01\n",
		"UBX-ACK-NAK:\n",
		"  clsID x06 msgID x01\n",
	} {
		messages = append(messages, parser.feed(line)...)
	}
	messages = append(messages, parser.flush()...)

	if assert.Len(t, messages, 2) {
		ack, ok := messages[0].Payload.(AckAck)
		require.True(t, ok)
		assert.Equal(t, uint8(0x06), ack.ClassID)
		assert.Equal(t, uint8(0x01), ack.MessageID)
		assert.Equal(t, AckAckType, messages[0].Type)

		nak, ok := messages[1].Payload.(AckNak)
		require.True(t, ok)
		assert.Equal(t, uint8(0x06), nak.ClassID)
		assert.Equal(t, uint8(0x01), nak.MessageID)
		assert.Equal(t, AckNakType, messages[1].Type)
	}
}

func TestParserHandlesMalformedNavFields(t *testing.T) {
	parser := newParser()
	for _, line := range []string{
		"UBX-NAV-STATUS:\n",
		"  iTOW truncated gpsFix bad flags xzz fixStat flags2\n",
		"UBX-NAV-CLOCK:\n",
		"  iTOW clkB bad clkD tAcc fAcc bad\n",
	} {
		parser.feed(line)
	}
	messages := parser.flush()
	require.Len(t, messages, 1)

	clock, ok := messages[0].Payload.(NavClock)
	require.True(t, ok)
	assert.Equal(t, unknownOffset, clock.Offset)
}

func Test_DefaultUblxCmds(t *testing.T) {
	cmds := defaultUblxCmds()

	// Flatten all args for easy searching
	var allArgs []string
	for _, cmd := range cmds {
		allArgs = append(allArgs, cmd.Args...)
	}

	// First command should disable all binary
	assert.Equal(t, disableBinary, cmds[0])

	// High-frequency NAV messages should be enabled at rate 1 (every second)
	for _, nav := range navEnableMsg {
		for _, bus := range ublxBusTypes {
			expected := fmt.Sprintf("CFG-MSGOUT-UBX_NAV_%s_%s,1", nav, bus)
			assert.Contains(t, allArgs, expected,
				"expected NAV %s enable at rate 1 on %s", nav, bus)
		}
	}

	// Low-frequency NAV messages (TIMELS) should be enabled at timeLSRateSeconds (every minute)
	for _, nav := range navSlowEnableMsg {
		for _, bus := range ublxBusTypes {
			expected := fmt.Sprintf("CFG-MSGOUT-UBX_NAV_%s_%s,%d", nav, bus, timeLSRateSeconds)
			assert.Contains(t, allArgs, expected,
				"expected NAV %s enable at rate %d on %s", nav, timeLSRateSeconds, bus)
		}
		// Also verify TIMELS is NOT enabled at rate 1
		for _, bus := range ublxBusTypes {
			notExpected := fmt.Sprintf("CFG-MSGOUT-UBX_NAV_%s_%s,1", nav, bus)
			assert.NotContains(t, allArgs, notExpected,
				"TIMELS should not be enabled at rate 1 on %s", bus)
		}
	}

	// NMEA should be enabled
	assert.Contains(t, cmds, enableNMEA)

	// All NMEA disable messages should be present on all bus types
	for _, nmea := range nmeaDisableMsg {
		for _, bus := range ublxBusTypes {
			expected := fmt.Sprintf("CFG-MSGOUT-NMEA_ID_%s_%s,0", nmea, bus)
			assert.Contains(t, allArgs, expected,
				"expected NMEA %s disable on %s", nmea, bus)
		}
	}
}
