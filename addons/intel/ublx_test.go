package intel

import (
	"errors"
	"strings"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ublox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockRunner implements ublox.Runner, recording all commands passed to it
// without executing anything. Tests verify command content at the Command
// level, not the raw exec level.
type mockRunner struct {
	commands      []ublox.Command
	defaultOutput string
	defaultErr    error
}

func (m *mockRunner) Run(cmd ublox.Command) (string, error) {
	m.commands = append(m.commands, cmd)
	return m.defaultOutput, m.defaultErr
}

func (m *mockRunner) Poll(cmd ublox.Command, responseType ublox.MessageType) (ublox.Message, error) {
	output, err := m.Run(cmd)
	return ublox.Message{Type: responseType, Raw: []string{output}}, err
}

func (m *mockRunner) RunAll(cmds ublox.CommandList, withSave bool) []string {
	var results []string
	for _, cmd := range cmds {
		output, err := m.Run(cmd)
		if cmd.ReportOutput {
			if err != nil {
				results = append(results, err.Error())
			} else {
				results = append(results, output)
			}
		}
	}
	if withSave {
		m.Run(ublox.SaveCommand) //nolint:errcheck // mock always succeeds for SAVE
	}
	return results
}

func (m *mockRunner) Save() error {
	_, err := m.Run(ublox.SaveCommand)
	return err
}

// containsCommand returns true if any recorded command's Args contain
// all the given substrings.
func (m *mockRunner) containsCommand(substrs ...string) bool {
	for _, cmd := range m.commands {
		joined := strings.Join(cmd.Args, " ")
		allFound := true
		for _, s := range substrs {
			if !strings.Contains(joined, s) {
				allFound = false
				break
			}
		}
		if allFound {
			return true
		}
	}
	return false
}

// setupCommandMock replaces NewCommandRunnerFn with a mock runner.
// No ExecCommand stubbing is needed.
func setupCommandMock() (*mockRunner, func()) {
	orig := ublox.NewCommandRunnerFn
	mock := &mockRunner{defaultOutput: "output"}
	ublox.NewCommandRunnerFn = func() (ublox.Runner, error) {
		return mock, nil
	}
	return mock, func() { ublox.NewCommandRunnerFn = orig }
}

func Test_UbxCmdListRunAll(t *testing.T) {
	mock, restore := setupCommandMock()
	defer restore()

	cmdList := UblxCmdList{
		{ReportOutput: false, Args: []string{"arg1"}},
		{ReportOutput: true, Args: []string{"-w", "2", "arg2"}},
		{ReportOutput: false, Args: []string{"arg3"}},
		{ReportOutput: true, Args: []string{"arg4"}},
	}

	results := cmdList.RunAll(true)

	// Should have 2 reportable results
	require.Equal(t, 2, len(results))
	assert.Equal(t, "output", results[0])
	assert.Equal(t, "output", results[1])

	// Verify all commands were executed
	assert.True(t, mock.containsCommand("arg1"))
	assert.True(t, mock.containsCommand("arg2"))
	assert.True(t, mock.containsCommand("arg3"))
	assert.True(t, mock.containsCommand("arg4"))
	assert.True(t, mock.containsCommand("SAVE"))
}

func Test_UbxCmdListRunAll_Error(t *testing.T) {
	mock, restore := setupCommandMock()
	defer restore()
	mock.defaultErr = errors.New("exec failed")

	cmdList := UblxCmdList{
		{ReportOutput: true, Args: []string{"will-fail"}},
	}

	results := cmdList.RunAll(false)
	require.Equal(t, 1, len(results))
	assert.Contains(t, results[0], "exec failed")
}
