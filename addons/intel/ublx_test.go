package intel

import (
	"errors"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

type mockExecutor struct {
	actualCalls   []actualCall
	expectedCalls []expectedCall
	defaultResult execResult
}

type actualCall struct {
	name string
	args []string
}

type expectedCall struct {
	args       []string
	returnData []byte
	returnErr  error
}

type execResult struct {
	data []byte
	err  error
}

func (m *mockExecutor) run(name string, arg ...string) ([]byte, error) {
	m.actualCalls = append(m.actualCalls, actualCall{name, arg})
	for _, expected := range m.expectedCalls {
		if slices.Equal(expected.args, arg) {
			return expected.returnData, expected.returnErr
		}
	}
	return m.defaultResult.data, m.defaultResult.err
}

func (m *mockExecutor) setDefaults(data string, err error) {
	m.defaultResult.data = []byte(data)
	m.defaultResult.err = err
}

func (m *mockExecutor) expect(args []string, data string, err error) {
	m.expectedCalls = append(m.expectedCalls, expectedCall{
		args:       args,
		returnData: []byte(data),
		returnErr:  err,
	})
}

// setupExecMock sets up a mock executor (returns the executor and the restoration function)
func setupExecMock() (*mockExecutor, func()) {
	originalExec := execCombined
	execMockData := &mockExecutor{}
	execCombined = execMockData.run
	return execMockData, func() { execCombined = originalExec }
}

func Test_UbxCmdRun(t *testing.T) {
	execMock, execRestore := setupExecMock()
	defer execRestore()
	execMock.setDefaults("result", nil)

	cmd := UblxCmd{Args: []string{"arg"}}
	stdout, err := cmd.run()
	assert.NoError(t, err)
	assert.Equal(t, "result", stdout)
	assert.Equal(t, 1, len(execMock.actualCalls))

	execMock.setDefaults("", errors.New("Error"))
	_, err = cmd.run()
	assert.Error(t, err)
	assert.Equal(t, 2, len(execMock.actualCalls))
}

func Test_UbxCmdListRunAll(t *testing.T) {
	execMock, execRestore := setupExecMock()
	defer execRestore()
	execMock.setDefaults("", errors.New("Unexpected call"))
	execMock.expect([]string{"arg1"}, "result1", nil)
	execMock.expect([]string{"arg2"}, "result2", nil)
	execMock.expect([]string{"arg3"}, "", errors.New("error3"))
	execMock.expect([]string{"arg4"}, "", errors.New("error4"))

	cmdList := UblxCmdList{
		UblxCmd{
			ReportOutput: false,
			Args:         []string{"arg1"},
		},
		UblxCmd{
			ReportOutput: true,
			Args:         []string{"arg2"},
		},
		UblxCmd{
			ReportOutput: false,
			Args:         []string{"arg3"},
		},
		UblxCmd{
			ReportOutput: true,
			Args:         []string{"arg4"},
		},
	}

	results := cmdList.runAll()
	assert.Equal(t, []string{"result2", "error4"}, results)
}

func Test_CmdDisableNmeaMsg(t *testing.T) {
	expected := []string{
		"CFG-MSGOUT-NMEA_ID_FOO_I2C,0",
		"CFG-MSGOUT-NMEA_ID_FOO_UART1,0",
		"CFG-MSGOUT-NMEA_ID_FOO_UART2,0",
		"CFG-MSGOUT-NMEA_ID_FOO_USB,0",
		"CFG-MSGOUT-NMEA_ID_FOO_SPI,0",
	}
	found := make([]string, 0, len(expected))
	cmds := cmdDisableNmeaMsg("FOO")
	assert.Equal(t, len(expected), len(cmds))
	for _, cmd := range cmds {
		assert.Equal(t, "-z", cmd.Args[0])
		if slices.Contains(expected, cmd.Args[1]) {
			found = append(found, cmd.Args[1])
		}
	}
	slices.Sort(expected)
	slices.Sort(found)
	assert.Equal(t, expected, found)
}
