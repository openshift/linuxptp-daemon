package ublox

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test constants to avoid goconst warnings
const (
	testProtoVersion  = "29.20"
	testProtoVersion2 = "29.25"
	testAntVoltCfg    = "CFG-HW-ANT_CFG_VOLTCTRL,1"
	testZCommand      = "UBX-CFG-VALSET,0"
	testGPS           = "GPS"
	testMonHw         = "MON-HW"
	testBinary        = "BINARY"
)

type execCall struct {
	args []string
}

type execMock struct {
	calls         []execCall
	defaultOutput string
	defaultErr    error
	expectations  []execExpectation
}

type execExpectation struct {
	matchArgs []string
	output    string
	err       error
}

func (m *execMock) run(args ...string) ([]byte, error) {
	m.calls = append(m.calls, execCall{args: slices.Clone(args)})
	for _, exp := range m.expectations {
		needle := strings.Join(exp.matchArgs, "::")
		haystack := strings.Join(args, "::")
		if strings.Contains(haystack, needle) {
			return []byte(exp.output), exp.err
		}
	}
	return []byte(m.defaultOutput), m.defaultErr
}

func setupExecMock() (*execMock, func()) {
	orig := execCommand
	mock := &execMock{defaultOutput: "OK"}
	execCommand = mock.run
	return mock, func() { execCommand = orig }
}

func TestBuildArgs(t *testing.T) {
	t.Run("injects protocol version and wait", func(t *testing.T) {
		r := &CommandRunner{protoVersion: testProtoVersion}
		args := r.buildArgs([]string{"-z", testAntVoltCfg})
		assert.Equal(t, []string{"-P", testProtoVersion, "-w", DefaultWait, "-z", testAntVoltCfg}, args)
	})

	t.Run("skips -P when version is empty", func(t *testing.T) {
		r := &CommandRunner{}
		args := r.buildArgs([]string{"-z", testAntVoltCfg})
		assert.Equal(t, []string{"-w", DefaultWait, "-z", testAntVoltCfg}, args)
	})

	t.Run("skips -P when already in args", func(t *testing.T) {
		r := &CommandRunner{protoVersion: testProtoVersion}
		args := r.buildArgs([]string{"-P", testProtoVersion2, "-z", testAntVoltCfg})
		assert.Equal(t, []string{"-w", DefaultWait, "-P", testProtoVersion2, "-z", testAntVoltCfg}, args)
	})

	t.Run("skips -w when already in args", func(t *testing.T) {
		r := &CommandRunner{protoVersion: testProtoVersion}
		args := r.buildArgs([]string{"-w", "5", "-e", "SURVEYIN,600,50000"})
		assert.Equal(t, []string{"-P", testProtoVersion, "-w", "5", "-e", "SURVEYIN,600,50000"}, args)
	})

	t.Run("empty version and user-supplied -w", func(t *testing.T) {
		r := &CommandRunner{}
		args := r.buildArgs([]string{"-P", testProtoVersion2, "-w", "2", "arg"})
		assert.Equal(t, []string{"-P", testProtoVersion2, "-w", "2", "arg"}, args)
	})
}

func TestCommandRunnerRun(t *testing.T) {
	mock, restore := setupExecMock()
	defer restore()

	r := &CommandRunner{protoVersion: testProtoVersion}

	t.Run("success", func(t *testing.T) {
		result, err := r.Run(Command{Args: []string{"-e", testGPS}})
		require.NoError(t, err)
		assert.Equal(t, "OK", result)
		assert.Equal(t, 1, len(mock.calls))
		assert.Equal(t, []string{"-P", testProtoVersion, "-w", DefaultWait, "-e", testGPS}, mock.calls[0].args)
	})

	t.Run("error", func(t *testing.T) {
		mock.calls = nil
		mock.defaultErr = errors.New("failed")
		_, err := r.Run(Command{Args: []string{"-e", testGPS}})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed")
		mock.defaultErr = nil
	})
}

func TestCommandRunnerRunWithReceiverNonACKUsesDirect(t *testing.T) {
	mock, restore := setupExecMock()
	defer restore()

	issueCalled := false
	runner := &CommandRunner{
		protoVersion: testProtoVersion,
		receiver:     &UBlox{broker: newMessageBroker()},
		issueFn: func(_ context.Context, _ Command) (<-chan error, error) {
			issueCalled = true
			return nil, nil
		},
	}

	output, err := runner.Run(Command{Args: []string{"-v", "1"}})
	require.NoError(t, err)
	assert.Equal(t, "OK", output)
	assert.False(t, issueCalled)
	assert.Equal(t, []string{"-P", testProtoVersion, "-w", DefaultWait, "-v", "1"}, mock.calls[0].args)
}

func TestCommandRunnerRunWithBrokerACKs(t *testing.T) {
	receiver := &UBlox{broker: newMessageBroker()}
	runner := &CommandRunner{
		protoVersion: testProtoVersion,
		receiver:     receiver,
		issueFn: func(_ context.Context, cmd Command) (<-chan error, error) {
			ackCount, _ := ackBatchExpectation(ackCommandGroups(cmd.Args))
			for range ackCount {
				receiver.broker.Publish(Message{Type: AckAckType, Payload: AckAck{}})
			}
			done := make(chan error, 1)
			done <- nil
			close(done)
			return done, nil
		},
	}

	output, err := runner.Run(Command{Args: []string{"-z", testZCommand, "-e", testGPS}})
	assert.NoError(t, err)
	assert.Empty(t, output)
}

func TestCommandRunnerRejectsMixedIndeterminateBatch(t *testing.T) {
	issueCalled := false
	runner := &CommandRunner{
		receiver: &UBlox{broker: newMessageBroker()},
		issueFn: func(_ context.Context, _ Command) (<-chan error, error) {
			issueCalled = true
			return nil, nil
		},
	}

	_, err := runner.Run(Command{Args: []string{"-d", testBinary, "-z", testZCommand}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot combine indeterminate ACK operation")
	assert.False(t, issueCalled)

	_, err = runner.Run(Command{Args: []string{"-d", testBinary, "-p", testMonHw}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot combine indeterminate ACK operation")
	assert.False(t, issueCalled)
}

func TestCollectAckResponsesStopsOnNonAck(t *testing.T) {
	messages := make(chan Message, 3)
	messages <- Message{Type: AckAckType}
	messages <- Message{Type: AckNakType}
	messages <- Message{Type: NavClockType}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	responses, err := collectAckResponses(ctx, messages, nil, 1, true)
	require.NoError(t, err)
	assert.Len(t, responses, 2)
}

func TestCollectAckResponsesWaitsForFirstAckBeforeStoppingOnNonAck(t *testing.T) {
	messages := make(chan Message, 2)
	messages <- Message{Type: NavClockType}
	messages <- Message{Type: AckAckType}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	responses, err := collectAckResponses(ctx, messages, nil, 1, true)
	require.NoError(t, err)
	assert.Len(t, responses, 1)
}

func TestCollectAckResponsesStopsAtExpectedCount(t *testing.T) {
	messages := make(chan Message, 2)
	messages <- Message{Type: AckAckType}
	messages <- Message{Type: AckNakType}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	responses, err := collectAckResponses(ctx, messages, nil, 2, false)
	require.NoError(t, err)
	assert.Len(t, responses, 2)
}

func TestCollectAckResponsesIgnoresNonAckForDeterminateBatch(t *testing.T) {
	messages := make(chan Message, 3)
	messages <- Message{Type: AckAckType}
	messages <- Message{Type: NavClockType}
	messages <- Message{Type: AckNakType}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	responses, err := collectAckResponses(ctx, messages, nil, 2, false)
	require.NoError(t, err)
	assert.Len(t, responses, 2)
}

func TestCommandRunnerRunCollectsUnknownAckBatch(t *testing.T) {
	receiver := &UBlox{broker: newMessageBroker()}
	runner := &CommandRunner{
		receiver: receiver,
		issueFn: func(_ context.Context, _ Command) (<-chan error, error) {
			receiver.broker.Publish(Message{Type: AckAckType, Payload: AckAck{}})
			receiver.broker.Publish(Message{Type: AckAckType, Payload: AckAck{}})
			done := make(chan error, 1)
			done <- nil
			close(done)
			return done, nil
		},
	}

	_, err := runner.Run(Command{Args: []string{"-d", testBinary}})
	assert.NoError(t, err)
}

func TestCommandRunnerRunWithBrokerReportOutput(t *testing.T) {
	receiver := &UBlox{broker: newMessageBroker()}
	runner := &CommandRunner{
		receiver: receiver,
		issueFn: func(_ context.Context, _ Command) (<-chan error, error) {
			receiver.broker.Publish(Message{
				Type:    AckAckType,
				Payload: AckAck{},
				Raw:     []string{"UBX-ACK-ACK:", "  clsID 0x06 msgID 0x01"},
			})
			done := make(chan error, 1)
			done <- nil
			close(done)
			return done, nil
		},
	}

	output, err := runner.Run(Command{Args: []string{"-e", "SURVEYIN,60,1"}, ReportOutput: true})
	require.NoError(t, err)
	assert.Equal(t, "UBX-ACK-ACK:\n  clsID 0x06 msgID 0x01", output)
}

func TestCommandRunnerRunWithBrokerNAK(t *testing.T) {
	receiver := &UBlox{broker: newMessageBroker()}
	runner := &CommandRunner{
		receiver: receiver,
		issueFn: func(_ context.Context, _ Command) (<-chan error, error) {
			receiver.broker.Publish(Message{Type: AckNakType, Payload: AckNak{}})
			done := make(chan error, 1)
			done <- nil
			close(done)
			return done, nil
		},
	}

	_, err := runner.Run(Command{Args: []string{"-z", testZCommand}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ACK-NAK")
	var nakErr *CommandNAKError
	require.ErrorAs(t, err, &nakErr)
	assert.Equal(t, 1, nakErr.Count)
}

func TestCommandRunnerRunSaveUsesACK(t *testing.T) {
	receiver := &UBlox{broker: newMessageBroker()}
	runner := &CommandRunner{
		receiver: receiver,
		issueFn: func(_ context.Context, _ Command) (<-chan error, error) {
			receiver.broker.Publish(Message{Type: AckAckType, Payload: AckAck{}})
			done := make(chan error, 1)
			done <- nil
			close(done)
			return done, nil
		},
	}

	_, err := runner.Run(SaveCommand)
	assert.NoError(t, err)
}

func TestCommandRunnerPoll(t *testing.T) {
	receiver := &UBlox{broker: newMessageBroker()}
	runner := &CommandRunner{
		receiver: receiver,
		issueFn: func(_ context.Context, _ Command) (<-chan error, error) {
			receiver.broker.Publish(Message{
				Type: MonHWType,
				Payload: RawMessage{
					Type:  MonHWType,
					Lines: []string{"UBX-MON-HW:", "  pin 1"},
				},
				Raw: []string{"UBX-MON-HW:", "  pin 1"},
			})
			done := make(chan error, 1)
			done <- nil
			close(done)
			return done, nil
		},
	}

	message, err := runner.Poll(Command{Args: []string{"-p", testMonHw}}, MonHWType)
	require.NoError(t, err)
	assert.Equal(t, MonHWType, message.Type)
	assert.Equal(t, []string{"UBX-MON-HW:", "  pin 1"}, message.Raw)
}

func TestPollResponseType(t *testing.T) {
	responseType, err := pollResponseType([]string{"-w", QueryTimeout, "-p", testMonHw})
	require.NoError(t, err)
	assert.Equal(t, MonHWType, responseType)

	responseType, err = pollResponseType([]string{"-p", "UBX-MON-HW"})
	require.NoError(t, err)
	assert.Equal(t, MonHWType, responseType)

	_, err = pollResponseType([]string{"-w", QueryTimeout})
	assert.Error(t, err)
}

func TestSaveUsesAckPath(t *testing.T) {
	commandAcks := ackCommandGroups(SaveCommand.Args)
	assert.NotEmpty(t, commandAcks)
	ackCount, indeterminate := ackBatchExpectation(commandAcks)
	assert.Equal(t, 1, ackCount)
	assert.False(t, indeterminate)
	commandAcks = ackCommandGroups([]string{"-p", testMonHw})
	assert.Empty(t, commandAcks)
	ackCount, indeterminate = ackBatchExpectation(commandAcks)
	assert.Equal(t, 0, ackCount)
	assert.False(t, indeterminate)
	args := []string{"-z", testZCommand, "-z", "two", "-e", testGPS, "-d", "GALILEO"}
	commandAcks = ackCommandGroups(args)
	ackCount, indeterminate = ackBatchExpectation(commandAcks)
	assert.Equal(t, 4, ackCount)
	assert.False(t, indeterminate)
	commandAcks = ackCommandGroups([]string{"-d", testGPS})
	ackCount, indeterminate = ackBatchExpectation(commandAcks)
	assert.Equal(t, 2, ackCount)
	assert.False(t, indeterminate)

	commandAcks = ackCommandGroups([]string{"-d", testBinary})
	ackCount, indeterminate = ackBatchExpectation(commandAcks)
	assert.Equal(t, 1, ackCount)
	assert.True(t, indeterminate)
}

func TestShortAckCommandDescription(t *testing.T) {
	assert.Equal(t, "-e GPS", shortAckCommandDescription("-e GPS"))
	description := shortAckCommandDescription("-z one -z two -z three -z four -z five -z six")
	assert.Len(t, description, 40)
	assert.True(t, strings.HasSuffix(description, "..."))
}

func TestNormalizeReportedOutput(t *testing.T) {
	input := "\nUBX-MON-HW:\n  pin 1\n\n  pin 2\r\n\n"
	assert.Equal(t, "UBX-MON-HW:\n  pin 1\n  pin 2", normalizeReportedOutput(input))
}

func TestCommandRunnerRunAll(t *testing.T) {
	mock, restore := setupExecMock()
	defer restore()
	mock.expectations = []execExpectation{
		{matchArgs: []string{"-w", DefaultWait, "fail"}, output: "error output", err: errors.New("cmd failed")},
	}

	r := &CommandRunner{}

	cmds := CommandList{
		{Args: []string{"cmd1"}, ReportOutput: false},
		{Args: []string{"cmd2"}, ReportOutput: true},
		{Args: []string{"fail"}, ReportOutput: true},
		{Args: []string{"cmd4"}, ReportOutput: false},
	}

	results := r.RunAll(cmds, false)

	// cmd2 reports "OK", fail reports error
	require.Equal(t, 2, len(results))
	assert.Equal(t, "OK", results[0])
	assert.Contains(t, results[1], "cmd failed")
	assert.Equal(t, 4, len(mock.calls))
}

func TestCommandRunnerRunAllWithSave(t *testing.T) {
	mock, restore := setupExecMock()
	defer restore()

	r := &CommandRunner{protoVersion: testProtoVersion}

	cmds := CommandList{
		{Args: []string{"-e", testGPS}},
	}

	_ = r.RunAll(cmds, true)

	// Should have 2 calls: the command + SAVE
	require.Equal(t, 2, len(mock.calls))
	lastArgs := mock.calls[1].args
	assert.True(t, slices.Contains(lastArgs, "SAVE"), "last call should be SAVE, got: %v", lastArgs)
}

func TestCommandListRunAll(t *testing.T) {
	mock, restore := setupExecMock()
	defer restore()
	mock.expectations = []execExpectation{
		{matchArgs: []string{"-p", "MON-VER"}, output: "PROTVER=29.20"},
	}

	cmds := CommandList{
		{Args: []string{"-P", testProtoVersion2, "-z", testAntVoltCfg}, ReportOutput: false},
		{Args: []string{"-P", testProtoVersion2, "-e", testGPS}, ReportOutput: true},
	}

	results := cmds.RunAll(true)

	// cmd2 reports output, plus SAVE at end
	require.Equal(t, 1, len(results))
	assert.Equal(t, "OK", results[0])

	// 3 calls: 2 commands + SAVE
	assert.Equal(t, 4, len(mock.calls))

	// No extra -P injection since commands already have -P
	for _, call := range mock.calls[1:3] {
		count := 0
		for _, arg := range call.args {
			if arg == "-P" {
				count++
			}
		}
		assert.Equal(t, 1, count, "should not double-inject -P: %v", call.args)
	}
}

func TestNewCommandRunner(t *testing.T) {
	mock, restore := setupExecMock()
	defer restore()

	mock.defaultOutput = "PROTVER=29.20"
	runner, err := NewCommandRunner()
	require.NoError(t, err)
	assert.Equal(t, testProtoVersion, runner.protoVersion)
	assert.Equal(t, 1, len(mock.calls))
}
