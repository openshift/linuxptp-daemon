package intel

import (
	"testing"

	dpll "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	"github.com/stretchr/testify/assert"
)

const allPinCaps = dpll.PinCapDir | dpll.PinCapPrio | dpll.PinCapState

func makeTwoParentPin(id uint32, label string, clockID uint64, eecDir, ppsDir uint32) *dpll.PinInfo {
	return &dpll.PinInfo{
		ID:           id,
		BoardLabel:   label,
		ClockID:      clockID,
		Type:         dpll.PinTypeEXT,
		Capabilities: allPinCaps,
		ParentDevice: []dpll.PinParentDevice{
			{ParentID: 100, Direction: eecDir, Prio: toPtr[uint32](0), State: dpll.PinStateDisconnected},
			{ParentID: 200, Direction: ppsDir, Prio: toPtr[uint32](0), State: dpll.PinStateDisconnected},
		},
	}
}

func makePins(pins ...*dpll.PinInfo) dpllPins {
	return dpllPins(pins)
}

func TestGetByLabel(t *testing.T) {
	pins := makePins(
		makeTwoParentPin(1, "SMA1", 1000, dpll.PinDirectionInput, dpll.PinDirectionInput),
		makeTwoParentPin(2, "SMA2", 1000, dpll.PinDirectionOutput, dpll.PinDirectionOutput),
		makeTwoParentPin(3, "SMA1", 2000, dpll.PinDirectionInput, dpll.PinDirectionInput),
	)

	t.Run("found", func(t *testing.T) {
		pin := pins.GetByLabel("SMA1", 1000)
		assert.NotNil(t, pin)
		assert.Equal(t, uint32(1), pin.ID)
	})

	t.Run("found_different_clockID", func(t *testing.T) {
		pin := pins.GetByLabel("SMA1", 2000)
		assert.NotNil(t, pin)
		assert.Equal(t, uint32(3), pin.ID)
	})

	t.Run("wrong_label", func(t *testing.T) {
		pin := pins.GetByLabel("GNSS_1PPS_IN", 1000)
		assert.Nil(t, pin)
	})

	t.Run("wrong_clockID", func(t *testing.T) {
		pin := pins.GetByLabel("SMA1", 9999)
		assert.Nil(t, pin)
	})
}

func TestGetCommandsForPluginPinSet_BadFormat(t *testing.T) {
	pins := makePins(
		makeTwoParentPin(1, "SMA1", 1000, dpll.PinDirectionInput, dpll.PinDirectionInput),
	)

	t.Run("single_value", func(t *testing.T) {
		cmds := pins.GetCommandsForPluginPinSet(1000, pinSet{"SMA1": "1"})
		assert.Empty(t, cmds)
	})

	t.Run("three_values", func(t *testing.T) {
		cmds := pins.GetCommandsForPluginPinSet(1000, pinSet{"SMA1": "1 1 1"})
		assert.Empty(t, cmds)
	})

	t.Run("empty_value", func(t *testing.T) {
		cmds := pins.GetCommandsForPluginPinSet(1000, pinSet{"SMA1": ""})
		assert.Empty(t, cmds)
	})
}

func TestGetCommandsForPluginPinSet_PinNotFound(t *testing.T) {
	pins := makePins(
		makeTwoParentPin(1, "SMA1", 1000, dpll.PinDirectionInput, dpll.PinDirectionInput),
	)

	cmds := pins.GetCommandsForPluginPinSet(1000, pinSet{"SMA2": "1 1"})
	assert.Empty(t, cmds)
}

func TestGetCommandsForPluginPinSet_NoParentDevices(t *testing.T) {
	pins := makePins(&dpll.PinInfo{
		ID:           1,
		BoardLabel:   "SMA1",
		ClockID:      1000,
		ParentDevice: []dpll.PinParentDevice{},
	})

	cmds := pins.GetCommandsForPluginPinSet(1000, pinSet{"SMA1": "1 1"})
	assert.Empty(t, cmds)
}

func TestGetCommandsForPluginPinSet_InvalidValue(t *testing.T) {
	pins := makePins(
		makeTwoParentPin(1, "SMA1", 1000, dpll.PinDirectionInput, dpll.PinDirectionInput),
	)

	// The default branch's `continue` only skips the inner parent-device loop,
	// so SetPinControlData is still called with a zero-valued PinParentControl.
	cmds := pins.GetCommandsForPluginPinSet(1000, pinSet{"SMA1": "9 9"})
	assert.Len(t, cmds, 1)
	cmd := cmds[0]
	assert.Len(t, cmd.PinParentCtl, 2)
	for _, pc := range cmd.PinParentCtl {
		assert.Nil(t, pc.Direction)
		assert.NotNil(t, pc.Prio)
		assert.Equal(t, uint32(0), *pc.Prio)
		assert.NotNil(t, pc.State, "input pins always get state=selectable")
		assert.Equal(t, uint32(dpll.PinStateSelectable), *pc.State)
	}
}

func toPtr[V any](v V) *V { return &v }

func TestGetCommandsForPluginPinSet_Value0_FromConnected(t *testing.T) {
	pins := makePins(&dpll.PinInfo{
		ID:           1,
		BoardLabel:   "SMA1",
		ClockID:      1000,
		Capabilities: allPinCaps,
		ParentDevice: []dpll.PinParentDevice{
			{ParentID: 100, Direction: dpll.PinDirectionInput, Prio: toPtr[uint32](0), State: dpll.PinStateConnected},
			{ParentID: 200, Direction: dpll.PinDirectionInput, Prio: toPtr[uint32](0), State: dpll.PinStateConnected},
		},
	})

	cmds := pins.GetCommandsForPluginPinSet(1000, pinSet{"SMA1": "0 0"})
	assert.Len(t, cmds, 1)
	cmd := cmds[0]
	assert.Equal(t, uint32(1), cmd.ID)
	assert.Len(t, cmd.PinParentCtl, 2)

	for _, pc := range cmd.PinParentCtl {
		assert.Nil(t, pc.Direction, "direction should be nil for value 0")
		assert.NotNil(t, pc.Prio)
		assert.Equal(t, uint32(PriorityDisabled), *pc.Prio)
		assert.NotNil(t, pc.State, "input pins always get state=selectable")
		assert.Equal(t, uint32(dpll.PinStateSelectable), *pc.State)
	}
}

func TestGetCommandsForPluginPinSet_Value0_FromDisconnected(t *testing.T) {
	pins := makePins(&dpll.PinInfo{
		ID:           1,
		BoardLabel:   "SMA1",
		ClockID:      1000,
		Capabilities: allPinCaps,
		ParentDevice: []dpll.PinParentDevice{
			{ParentID: 100, Direction: dpll.PinDirectionOutput, Prio: toPtr[uint32](0), State: dpll.PinStateDisconnected},
			{ParentID: 200, Direction: dpll.PinDirectionOutput, Prio: toPtr[uint32](0), State: dpll.PinStateDisconnected},
		},
	})

	cmds := pins.GetCommandsForPluginPinSet(1000, pinSet{"SMA1": "0 0"})
	assert.Len(t, cmds, 1)
	cmd := cmds[0]
	assert.Len(t, cmd.PinParentCtl, 2)

	for _, pc := range cmd.PinParentCtl {
		assert.Nil(t, pc.Direction, "direction should be nil for value 0")
		// parentDevice.Direction is Output, so State is set
		assert.NotNil(t, pc.State)
		assert.Equal(t, uint32(dpll.PinStateDisconnected), *pc.State)
		assert.Nil(t, pc.Prio, "prio should not be set for output direction")
	}
}

func TestGetCommandsForPluginPinSet_Value1_SetInput(t *testing.T) {
	pins := makePins(&dpll.PinInfo{
		ID:           5,
		BoardLabel:   "SMA1",
		ClockID:      1000,
		Capabilities: allPinCaps,
		ParentDevice: []dpll.PinParentDevice{
			{ParentID: 100, Direction: dpll.PinDirectionOutput, Prio: toPtr[uint32](0), State: dpll.PinStateDisconnected},
			{ParentID: 200, Direction: dpll.PinDirectionOutput, Prio: toPtr[uint32](0), State: dpll.PinStateDisconnected},
		},
	})

	cmds := pins.GetCommandsForPluginPinSet(1000, pinSet{"SMA1": "1 1"})
	assert.Len(t, cmds, 2, "direction change should produce two commands")

	dirCmd := cmds[0]
	assert.Equal(t, uint32(5), dirCmd.ID)
	assert.Len(t, dirCmd.PinParentCtl, 2)
	for _, pc := range dirCmd.PinParentCtl {
		assert.NotNil(t, pc.Direction, "first command sets direction")
		assert.Equal(t, uint32(dpll.PinDirectionInput), *pc.Direction)
		assert.Nil(t, pc.Prio, "first command has no prio")
		assert.Nil(t, pc.State, "first command has no state")
	}

	dataCmd := cmds[1]
	assert.Equal(t, uint32(5), dataCmd.ID)
	assert.Len(t, dataCmd.PinParentCtl, 2)
	for _, pc := range dataCmd.PinParentCtl {
		assert.Nil(t, pc.Direction, "second command has no direction")
		assert.NotNil(t, pc.Prio, "second command sets prio for input")
		assert.Equal(t, uint32(PriorityEnabled), *pc.Prio)
		assert.NotNil(t, pc.State, "input pins always get state=selectable")
		assert.Equal(t, uint32(dpll.PinStateSelectable), *pc.State)
	}
}

func TestGetCommandsForPluginPinSet_Value2_SetOutput(t *testing.T) {
	pins := makePins(&dpll.PinInfo{
		ID:           7,
		BoardLabel:   "SMA2",
		ClockID:      1000,
		Capabilities: allPinCaps,
		ParentDevice: []dpll.PinParentDevice{
			{ParentID: 100, Direction: dpll.PinDirectionInput, Prio: toPtr[uint32](0), State: dpll.PinStateConnected},
			{ParentID: 200, Direction: dpll.PinDirectionInput, Prio: toPtr[uint32](0), State: dpll.PinStateConnected},
		},
	})

	cmds := pins.GetCommandsForPluginPinSet(1000, pinSet{"SMA2": "2 2"})
	assert.Len(t, cmds, 2, "direction change should produce two commands")

	dirCmd := cmds[0]
	assert.Equal(t, uint32(7), dirCmd.ID)
	assert.Len(t, dirCmd.PinParentCtl, 2)
	for _, pc := range dirCmd.PinParentCtl {
		assert.NotNil(t, pc.Direction, "first command sets direction")
		assert.Equal(t, uint32(dpll.PinDirectionOutput), *pc.Direction)
		assert.Nil(t, pc.Prio, "first command has no prio")
		assert.Nil(t, pc.State, "first command has no state")
	}

	dataCmd := cmds[1]
	assert.Equal(t, uint32(7), dataCmd.ID)
	assert.Len(t, dataCmd.PinParentCtl, 2)
	for _, pc := range dataCmd.PinParentCtl {
		assert.Nil(t, pc.Direction, "second command has no direction")
		assert.NotNil(t, pc.State, "second command sets state for output")
		assert.Equal(t, uint32(dpll.PinStateConnected), *pc.State)
		assert.Nil(t, pc.Prio, "second command has no prio for output")
	}
}

func TestGetCommandsForPluginPinSet_MultiplePins(t *testing.T) {
	pins := makePins(
		makeTwoParentPin(1, "SMA1", 1000, dpll.PinDirectionInput, dpll.PinDirectionInput),
		makeTwoParentPin(2, "SMA2", 1000, dpll.PinDirectionOutput, dpll.PinDirectionOutput),
	)

	ps := pinSet{
		"SMA1": "1 1",
		"SMA2": "2 2",
	}
	cmds := pins.GetCommandsForPluginPinSet(1000, ps)
	assert.Len(t, cmds, 2)

	cmdByID := map[uint32]dpll.PinParentDeviceCtl{}
	for _, c := range cmds {
		cmdByID[c.ID] = c
	}

	sma1 := cmdByID[1]
	assert.Len(t, sma1.PinParentCtl, 2)
	for _, pc := range sma1.PinParentCtl {
		assert.Nil(t, pc.Direction, "no direction change for Input->Input")
		assert.NotNil(t, pc.Prio)
		assert.NotNil(t, pc.State, "input pins always get state=selectable")
		assert.Equal(t, uint32(dpll.PinStateSelectable), *pc.State)
	}

	sma2 := cmdByID[2]
	assert.Len(t, sma2.PinParentCtl, 2)
	for _, pc := range sma2.PinParentCtl {
		assert.Nil(t, pc.Direction, "no direction change for Output->Output")
		assert.NotNil(t, pc.State)
	}
}

func TestGetCommandsForPluginPinSet_ParentIDs(t *testing.T) {
	pins := makePins(&dpll.PinInfo{
		ID:           10,
		BoardLabel:   "SMA1",
		ClockID:      5000,
		Capabilities: allPinCaps,
		ParentDevice: []dpll.PinParentDevice{
			{ParentID: 42, Direction: dpll.PinDirectionInput, Prio: toPtr[uint32](0)},
			{ParentID: 99, Direction: dpll.PinDirectionInput, Prio: toPtr[uint32](0)},
		},
	})

	cmds := pins.GetCommandsForPluginPinSet(5000, pinSet{"SMA1": "1 1"})
	assert.Len(t, cmds, 1)
	assert.Equal(t, uint32(42), cmds[0].PinParentCtl[0].PinParentID)
	assert.Equal(t, uint32(99), cmds[0].PinParentCtl[1].PinParentID)
}

func TestGetCommandsForPluginPinSet_WhitespaceHandling(t *testing.T) {
	pins := makePins(
		makeTwoParentPin(1, "SMA1", 1000, dpll.PinDirectionInput, dpll.PinDirectionInput),
	)

	t.Run("leading_trailing_spaces", func(t *testing.T) {
		cmds := pins.GetCommandsForPluginPinSet(1000, pinSet{"SMA1": "  1 1  "})
		assert.Len(t, cmds, 1)
	})

	t.Run("extra_internal_spaces", func(t *testing.T) {
		cmds := pins.GetCommandsForPluginPinSet(1000, pinSet{"SMA1": "1   1"})
		assert.Len(t, cmds, 1)
	})
}

func TestSetPinControlData_StateOnlyPin(t *testing.T) {
	pin := dpll.PinInfo{
		ID:           15,
		BoardLabel:   "U.FL1",
		Capabilities: dpll.PinCapState,
		ParentDevice: []dpll.PinParentDevice{
			{ParentID: 0, Direction: dpll.PinDirectionOutput, State: dpll.PinStateConnected},
			{ParentID: 1, Direction: dpll.PinDirectionOutput, State: dpll.PinStateConnected},
		},
	}

	control := PinParentControl{
		EecOutputState: dpll.PinStateDisconnected,
		PpsOutputState: dpll.PinStateDisconnected,
	}
	cmds := SetPinControlData(pin, control)
	assert.Len(t, cmds, 1)
	for _, pc := range cmds[0].PinParentCtl {
		assert.NotNil(t, pc.State, "state-can-change should allow State")
		assert.Equal(t, uint32(dpll.PinStateDisconnected), *pc.State)
		assert.Nil(t, pc.Prio, "no priority-can-change capability")
		assert.Nil(t, pc.Direction, "no direction-can-change capability")
	}
}

func TestSetPinControlData_StateOnlyPin_InputDirection(t *testing.T) {
	pin := dpll.PinInfo{
		ID:           37,
		BoardLabel:   "U.FL2",
		Capabilities: dpll.PinCapState,
		ParentDevice: []dpll.PinParentDevice{
			{ParentID: 2, Direction: dpll.PinDirectionInput, State: dpll.PinStateDisconnected},
			{ParentID: 3, Direction: dpll.PinDirectionInput, State: dpll.PinStateDisconnected},
		},
	}

	control := PinParentControl{
		EecPriority:    PriorityDisabled,
		PpsPriority:    PriorityDisabled,
		EecOutputState: dpll.PinStateDisconnected,
		PpsOutputState: dpll.PinStateDisconnected,
	}
	cmds := SetPinControlData(pin, control)
	assert.Len(t, cmds, 1)
	for _, pc := range cmds[0].PinParentCtl {
		assert.Nil(t, pc.Prio, "no priority-can-change: prio must not be set")
		assert.Nil(t, pc.Direction, "no direction-can-change: direction must not be set")
		assert.NotNil(t, pc.State, "input pin with state-can-change gets state=selectable")
		assert.Equal(t, uint32(dpll.PinStateSelectable), *pc.State)
	}
}

func TestSetPinControlData_NoCaps(t *testing.T) {
	pin := dpll.PinInfo{
		ID:           99,
		Capabilities: dpll.PinCapNone,
		ParentDevice: []dpll.PinParentDevice{
			{ParentID: 0, Direction: dpll.PinDirectionInput},
			{ParentID: 1, Direction: dpll.PinDirectionInput},
		},
	}

	control := PinParentControl{
		EecPriority:    0,
		PpsPriority:    0,
		EecOutputState: dpll.PinStateConnected,
		PpsOutputState: dpll.PinStateConnected,
	}
	cmds := SetPinControlData(pin, control)
	assert.Len(t, cmds, 1)
	for _, pc := range cmds[0].PinParentCtl {
		assert.Nil(t, pc.Prio, "no capabilities: prio must not be set")
		assert.Nil(t, pc.State, "no capabilities: state must not be set")
		assert.Nil(t, pc.Direction, "no capabilities: direction must not be set")
	}
}

func TestSetPinControlData_PrioCapButNilPrio(t *testing.T) {
	pin := dpll.PinInfo{
		ID:           58,
		BoardLabel:   "U.FL2",
		Capabilities: dpll.PinCapPrio | dpll.PinCapState,
		ParentDevice: []dpll.PinParentDevice{
			{ParentID: 4, Direction: dpll.PinDirectionInput, State: dpll.PinStateDisconnected},
			{ParentID: 5, Direction: dpll.PinDirectionInput, State: dpll.PinStateDisconnected},
		},
	}

	control := PinParentControl{
		EecPriority: PriorityDisabled,
		PpsPriority: PriorityDisabled,
	}
	cmds := SetPinControlData(pin, control)
	assert.Len(t, cmds, 1)
	for _, pc := range cmds[0].PinParentCtl {
		assert.Nil(t, pc.Prio, "kernel reports no prio on parent device: prio must not be set")
		assert.NotNil(t, pc.State, "input pin with state-can-change gets state=selectable")
		assert.Equal(t, uint32(dpll.PinStateSelectable), *pc.State)
	}
}

func TestBuildDirectionCmd_NoDirCap(t *testing.T) {
	eecDir := uint8(dpll.PinDirectionOutput)
	ppsDir := uint8(dpll.PinDirectionOutput)
	pin := dpll.PinInfo{
		ID:           10,
		Capabilities: dpll.PinCapPrio | dpll.PinCapState,
		ParentDevice: []dpll.PinParentDevice{
			{ParentID: 0, Direction: dpll.PinDirectionInput},
			{ParentID: 1, Direction: dpll.PinDirectionInput},
		},
	}
	control := PinParentControl{
		EecDirection: &eecDir,
		PpsDirection: &ppsDir,
	}
	cmd := buildDirectionCmd(&pin, control)
	assert.Nil(t, cmd, "direction change should be skipped without PinCapDir")
	assert.Equal(t, uint32(dpll.PinDirectionInput), pin.ParentDevice[0].Direction, "direction should not be updated")
	assert.Equal(t, uint32(dpll.PinDirectionInput), pin.ParentDevice[1].Direction, "direction should not be updated")
}

func TestApplyPinCommands(t *testing.T) {
	_, restorePins := setupMockDPLLPins(
		makeTwoParentPin(1, "SMA1", 1000, dpll.PinDirectionInput, dpll.PinDirectionInput),
	)
	defer restorePins()

	mockPinSet, restore := setupBatchPinSetMock()
	defer restore()

	cmds := []dpll.PinParentDeviceCtl{
		{
			ID: 1,
			PinParentCtl: []dpll.PinControl{
				{PinParentID: 100, Prio: toPtr[uint32](0)},
			},
		},
	}

	err := DpllPins.ApplyPinCommands(cmds)
	assert.NoError(t, err)
	assert.NotNil(t, mockPinSet.commands)
	assert.Len(t, *mockPinSet.commands, 1)
}
