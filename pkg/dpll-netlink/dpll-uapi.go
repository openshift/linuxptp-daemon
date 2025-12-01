// Definitions from <kernel-root>/include/uapi/linux/dpll.h and
// tools/net/ynl/generated/dpll-user.h

package dpll_netlink

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// DpllMCGRPMonitor defines DPLL subsystem multicast group name
const DpllMCGRPMonitor = "monitor"

// DpllPhaseOffsetDivider phase offset divider allows userspace to calculate a value of
// measured signal phase difference between a pin and dpll device
// as a fractional value with three digit decimal precision.
// Value of (DPLL_A_PHASE_OFFSET / DPLL_PHASE_OFFSET_DIVIDER) is an
// integer part of a measured phase offset value.
// Value of (DPLL_A_PHASE_OFFSET % DPLL_PHASE_OFFSET_DIVIDER) is a
// fractional part of a measured phase offset value.
const DpllPhaseOffsetDivider = 1000

// DpllTemperatureDivider allows userspace to calculate the
// temperature as float with three digit decimal precision.
// Value of (DPLL_A_TEMP / DPLL_TEMP_DIVIDER) is integer part of
// temperature value.
// Value of (DPLL_A_TEMP % DPLL_TEMP_DIVIDER) is fractional part of
// temperature value.
const DpllTemperatureDivider = 1000

// DpllAttributes provides the dpll_a attribute-set
const (
	DpllAttributes = iota
	DpllID
	DpllModuleName
	DpllAttPadding
	DpllClockID
	DpllMode
	DpllModeSupported
	DpllLockStatus
	DpllTemp
	DpllType
	DpllLockStatusError
	DpllClockQualityLevel
	DpllPhaseOffsetMonitor
	DpllPhaseOffsetAverageFactor
)

// DpllPinTypes defines the attribute-set for dpll_a_pin
const (
	// attribute-set dpll_a_pin
	DpllPinTypes = iota
	DpllPinID
	DpllPinParentID
	DpllPinModuleName
	DpllPinPadding
	DpllPinClockID
	DpllPinBoardLabel
	DpllPinPanelLabel
	DpllPinPackageLabel
	DpllPinType
	DpllPinDirection
	DpllPinFrequency
	DpllPinFrequencySupported
	DpllPinFrequencyMin
	DpllPinFrequencyMax
	DpllPinPrio
	DpllPinState
	DpllPinCapabilities
	DpllPinParentDevice
	DpllPinParentPin
	DpllPinPhaseAdjustMin
	DpllPinPhaseAdjustMax
	DpllPinPhaseAdjust
	DpllPinPhaseOffset
	DpllPinFractionalFrequencyOffset
	DpllPinEsyncFrequency
	DpllPinEsyncFrequencySupported
	DpllPinEsyncPulse
	DpllPinReferenceSync
	DpllPinPhaseAdjustGran
)

// DpllCmds defines DPLL subsystem commands encoding
const (
	DpllCmds = iota
	DpllCmdDeviceIDGet
	DpllCmdDeviceGet
	DpllCmdDeviceSet
	DpllCmdDeviceCreateNtf
	DpllCmdDeviceDeleteNtf
	DpllCmdDeviceChangeNtf
	DpllCmdPinIDGet
	DpllCmdPinGet
	DpllCmdPinSet
	DpllCmdPinCreateNtf
	DpllCmdPinDeleteNtf
	DpllCmdPinChangeNtf
)

// DpllLockStatusAttribute defines DPLL lock status encoding
const (
	DpllLockStatusAttribute = iota
	DpllLockStatusUnlocked
	DpllLockStatusLocked
	DpllLockStatusLockedHoldoverAcquired
	DpllLockStatusHoldover
)

// LockStatusErrorTypes defines device lock error types
const (
	LockStatusErrorTypes = iota
	LockStatusErrorNone
	LockStatusErrorUndefined
	// LockStatusErrorMediaDown indicates dpll device lock status was changed because of associated
	// media got down.
	// This may happen for example if dpll device was previously
	// locked on an input pin of type PIN_TYPE_SYNCE_ETH_PORT.
	LockStatusErrorMediaDown
	// LockStatusFFOTooHigh indicates the FFO (Fractional Frequency Offset) between the RX and TX
	// symbol rate on the media got too high.
	// This may happen for example if dpll device was previously
	// locked on an input pin of type PIN_TYPE_SYNCE_ETH_PORT.
	LockStatusFFOTooHigh
)

// GetLockStatusError returns lock status error as a string
func GetLockStatusError(le uint32) string {
	lockStatusErrorMap := map[uint32]string{
		LockStatusErrorNone:      "none",
		LockStatusErrorUndefined: "undefined",
		LockStatusErrorMediaDown: "media-down",
		LockStatusFFOTooHigh:     "fto-too-high",
	}
	return lockStatusErrorMap[le]
}

// PhaseOffsetMonitor defines phase offset monitor enable / disable states.
const (
	PhaseOffsetMonitorAttribute = iota
	PhaseOffsetMonitorEnabled
	PhaseOffsetMonitorDisabled
)

// GetPhaseOffsetMonitor returns phase offset monitor as a string
func GetPhaseOffsetMonitor(po uint32) string {
	phaseOffsetMonitorMap := map[uint32]string{
		PhaseOffsetMonitorEnabled:  "enabled",
		PhaseOffsetMonitorDisabled: "disabled",
	}
	return phaseOffsetMonitorMap[po]
}

// ClockQualityLevel defines possible clock quality levels when on holdover
const (
	ClockQualityLevel = iota
	ClockQualityLevelITUOpt1PRC
	ClockQualityLevelITUOpt1SSUA
	ClockQualityLevelITUOpt1SSUB
	ClockQualityLevelITUOpt1EEC1
	ClockQualityLevelITUOpt1PRTC
	ClockQualityLevelITUOpt1EPRTC
	ClockQualityLevelITUOpt1EEEC
	ClockQualityLevelItuOpt1EPRC
)

// GetClockQualityLevels returns clock quality levels as a string
func GetClockQualityLevels(cqs []uint32) string {
	clockQualityLevels := make([]string, 0)
	for _, cq := range cqs {
		clockQualityLevels = append(clockQualityLevels, GetClockQualityLevel(cq))
	}
	return strings.Join(clockQualityLevels, ",")
}

// GetClockQualityLevel returns clock quality level as a string
func GetClockQualityLevel(cq uint32) string {
	clockQualityLevelMap := map[uint32]string{
		ClockQualityLevel:             "itu-opt1-prc",
		ClockQualityLevelITUOpt1PRC:   "itu-opt1-prc",
		ClockQualityLevelITUOpt1SSUA:  "itu-opt1-ssua",
		ClockQualityLevelITUOpt1SSUB:  "itu-opt1-ssub",
		ClockQualityLevelITUOpt1EEC1:  "itu-opt1-eec1",
		ClockQualityLevelITUOpt1PRTC:  "itu-opt1-prtc",
		ClockQualityLevelITUOpt1EPRTC: "itu-opt1-eprtc",
		ClockQualityLevelITUOpt1EEEC:  "itu-opt1-eeec",
		ClockQualityLevelItuOpt1EPRC:  "itu-opt1-eprc",
	}
	cqStr, found := clockQualityLevelMap[cq]
	if found {
		return cqStr
	}
	return ""
}

// DpllTypeAttribute defines DPLL types
const (
	DpllTypeAttribute = iota
	// DpllTypePPS indicates dpll produces Pulse-Per-Second signal
	DpllTypePPS
	// DpllTypeEEC indicates dpll drives the Ethernet Equipment Clock
	DpllTypeEEC
)

// GetLockStatus returns DPLL lock status as a string
func GetLockStatus(ls uint32) string {
	lockStatusMap := map[uint32]string{
		DpllLockStatusUnlocked:               "unlocked",
		DpllLockStatusLocked:                 "locked",
		DpllLockStatusLockedHoldoverAcquired: "locked-ho-acquired",
		DpllLockStatusHoldover:               "holdover",
	}
	status, found := lockStatusMap[ls]
	if found {
		return status
	}
	return ""
}

// GetDpllType returns DPLL type as a string
func GetDpllType(tp uint32) string {
	typeMap := map[int]string{
		DpllTypePPS: "pps",
		DpllTypeEEC: "eec",
	}
	typ, found := typeMap[int(tp)]
	if found {
		return typ
	}
	return ""
}

// GetMode returns DPLL mode as a string
func GetMode(md uint32) string {
	modeMap := map[int]string{
		1: "manual",
		2: "automatic",
	}
	mode, found := modeMap[int(md)]
	if found {
		return mode
	}
	return ""
}

// DpllStatusHR represents human-readable DPLL status
type DpllStatusHR struct {
	Timestamp                time.Time `json:"timestamp"`
	ID                       uint32    `json:"id"`
	ModuleName               string    `json:"moduleName"`
	Mode                     string    `json:"mode"`
	ModeSupported            string    `json:"modeSupported"`
	LockStatus               string    `json:"lockStatus"`
	ClockID                  string    `json:"clockId"`
	Type                     string    `json:"type"`
	Temp                     float64   `json:"temp,omitempty"`
	LockStatusError          string    `json:"lockStatusError,omitempty"`
	ClockQualityLevel        string    `json:"clockQualityLevel,omitempty"`
	PhaseOffsetMonitor       string    `json:"phaseOffsetMonitor,omitempty"`
	PhaseOffsetAverageFactor uint32    `json:"phaseOffsetAverageFactor,omitempty"`
}

// GetDpllStatusHR returns human-readable DPLL status
func GetDpllStatusHR(reply *DoDeviceGetReply, timestamp time.Time) ([]byte, error) {
	var modes []string
	for _, md := range reply.ModeSupported {
		modes = append(modes, GetMode(md))
	}
	hr := DpllStatusHR{
		Timestamp:                timestamp,
		ID:                       reply.ID,
		ModuleName:               reply.ModuleName,
		Mode:                     GetMode(reply.Mode),
		ModeSupported:            fmt.Sprint(strings.Join(modes[:], ",")),
		LockStatus:               GetLockStatus(reply.LockStatus),
		ClockID:                  fmt.Sprintf("0x%x", reply.ClockID),
		Type:                     GetDpllType(reply.Type),
		Temp:                     float64(reply.Temp) / DpllTemperatureDivider,
		LockStatusError:          GetLockStatusError(reply.LockStatusError),
		ClockQualityLevel:        GetClockQualityLevels(reply.ClockQualityLevel),
		PhaseOffsetMonitor:       GetPhaseOffsetMonitor(reply.PhaseOffsetMonitor),
		PhaseOffsetAverageFactor: reply.PhaseOffsetAverageFactor,
	}
	return json.Marshal(hr)
}

// PinInfoHR is used with the DoPinGet method.
type PinInfoHR struct {
	Timestamp                 time.Time           `json:"timestamp"`
	ID                        uint32              `json:"id"`
	ModuleName                string              `json:"moduleName,omitempty"`
	ClockID                   string              `json:"clockId"`
	BoardLabel                string              `json:"boardLabel,omitempty"`
	PanelLabel                string              `json:"panelLabel,omitempty"`
	PackageLabel              string              `json:"packageLabel,omitempty"`
	Type                      string              `json:"type,omitempty"`
	Frequency                 uint64              `json:"frequency,omitempty"`
	FrequencySupported        []FrequencyRange    `json:"frequencySupported,omitempty"`
	Capabilities              string              `json:"capabilities,omitempty"`
	ParentDevice              []PinParentDeviceHR `json:"pinParentDevice,omitempty"`
	ParentPin                 []PinParentPinHR    `json:"pinParentPin,omitempty"`
	PhaseAdjustMin            int32               `json:"phaseAdjustMin,omitempty"`
	PhaseAdjustMax            int32               `json:"phaseAdjustMax,omitempty"`
	PhaseAdjust               int32               `json:"phaseAdjust"`
	FractionalFrequencyOffset int                 `json:"fractionalFrequencyOffset,omitempty"`
	EsyncFrequency            int64               `json:"esyncFrequency,omitempty"`
	EsyncFrequencySupported   []FrequencyRange    `json:"esyncFrequencySupported,omitempty"`
	EsyncPulse                int64               `json:"esyncPulse,omitempty"`
	ReferenceSync             []ReferenceSync     `json:"referenceSync,omitempty"`
	PhaseAdjustGran           uint32              `json:"phaseAdjustGran,omitempty"`
}

// PinParentDeviceHR contains nested netlink attributes.
type PinParentDeviceHR struct {
	ParentID      uint32  `json:"parentID"`
	Direction     string  `json:"direction"`
	Prio          uint32  `json:"prio"`
	State         string  `json:"state"`
	PhaseOffsetPs float64 `json:"phaseOffsetPs"`
}

// PinParentPin contains nested netlink attributes.
type PinParentPinHR struct {
	ParentID uint32 `json:"parentID"`
	State    string `json:"parentState"`
}

// Defines possible pin states
const (
	PinStateConnected    = 1
	PinStateDisconnected = 2
	PinStateSelectable   = 3
)

// GetPinState returns DPLL pin state as a string
func GetPinState(s uint32) string {
	stateMap := map[int]string{
		PinStateConnected:    "connected",
		PinStateDisconnected: "disconnected",
		PinStateSelectable:   "selectable",
	}
	r, found := stateMap[int(s)]
	if found {
		return r
	}
	return ""
}

// GetPinType returns DPLL pin type as a string
func GetPinType(tp uint32) string {
	typeMap := map[int]string{
		1: "mux",
		2: "ext",
		3: "synce-eth-port",
		4: "int-oscillator",
		5: "gnss",
	}
	typ, found := typeMap[int(tp)]
	if found {
		return typ
	}
	return ""
}

// Defines pin directions
const (
	PinDirectionInput  = 1
	PinDirectionOutput = 2
)

// GetPinDirection returns DPLL pin direction as a string
func GetPinDirection(d uint32) string {
	directionMap := map[int]string{
		PinDirectionInput:  "input",
		PinDirectionOutput: "output",
	}
	dir, found := directionMap[int(d)]
	if found {
		return dir
	}
	return ""
}

// GetPinCapabilities returns DPLL pin capabilities as a csv
func GetPinCapabilities(c uint32) string {
	cMap := map[int]string{
		0: "",
		1: "direction-can-change",
		2: "priority-can-change",
		3: "direction-can-change,priority-can-change",
		4: "state-can-change",
		5: "state-can-change,direction-can-change",
		6: "state-can-change,priority-can-change",
		7: "state-can-change,direction-can-change,priority-can-change",
	}
	cap, found := cMap[int(c)]
	if found {
		return cap
	}
	return ""
}

// GetPinInfoHR returns human-readable pin status
func GetPinInfoHR(reply *PinInfo, timestamp time.Time) ([]byte, error) {
	hr := PinInfoHR{
		Timestamp:                 timestamp,
		ID:                        reply.ID,
		ClockID:                   fmt.Sprintf("0x%x", reply.ClockID),
		BoardLabel:                reply.BoardLabel,
		PanelLabel:                reply.PanelLabel,
		PackageLabel:              reply.PackageLabel,
		Type:                      GetPinType(reply.Type),
		Frequency:                 reply.Frequency,
		FrequencySupported:        make([]FrequencyRange, 0),
		PhaseAdjustMin:            reply.PhaseAdjustMin,
		PhaseAdjustMax:            reply.PhaseAdjustMax,
		PhaseAdjust:               reply.PhaseAdjust,
		FractionalFrequencyOffset: reply.FractionalFrequencyOffset,
		ModuleName:                reply.ModuleName,
		ParentDevice:              make([]PinParentDeviceHR, 0),
		ParentPin:                 make([]PinParentPinHR, 0),
		Capabilities:              GetPinCapabilities(reply.Capabilities),
		EsyncFrequency:            reply.EsyncFrequency,
		EsyncFrequencySupported:   make([]FrequencyRange, 0),
		EsyncPulse:                int64(reply.EsyncPulse),
		ReferenceSync:             make([]ReferenceSync, 0),
		PhaseAdjustGran:           reply.PhaseAdjustGran,
	}
	for i := 0; i < len(reply.ParentDevice); i++ {
		hr.ParentDevice = append(hr.ParentDevice, PinParentDeviceHR{
			ParentID:      reply.ParentDevice[i].ParentID,
			Direction:     GetPinDirection(reply.ParentDevice[i].Direction),
			Prio:          reply.ParentDevice[i].Prio,
			State:         GetPinState(reply.ParentDevice[i].State),
			PhaseOffsetPs: float64(reply.ParentDevice[i].PhaseOffset) / DpllPhaseOffsetDivider,
		})

	}
	for i := 0; i < len(reply.ParentPin); i++ {
		hr.ParentPin = append(hr.ParentPin, PinParentPinHR{
			ParentID: reply.ParentPin[i].ParentID,
			State:    GetPinState(reply.ParentPin[i].State),
		})
	}
	for i := 0; i < len(reply.FrequencySupported); i++ {
		hr.FrequencySupported = append(hr.FrequencySupported, FrequencyRange{
			FrequencyMin: reply.FrequencySupported[i].FrequencyMin,
			FrequencyMax: reply.FrequencySupported[i].FrequencyMax,
		})
	}
	for i := 0; i < len(reply.EsyncFrequencySupported); i++ {
		hr.EsyncFrequencySupported = append(hr.EsyncFrequencySupported, FrequencyRange{
			FrequencyMin: reply.EsyncFrequencySupported[i].FrequencyMin,
			FrequencyMax: reply.EsyncFrequencySupported[i].FrequencyMax,
		})
	}
	for i := 0; i < len(reply.ReferenceSync); i++ {
		hr.ReferenceSync = append(hr.ReferenceSync, ReferenceSync{
			ID:    reply.ReferenceSync[i].ID,
			State: reply.ReferenceSync[i].State,
		})
	}
	return json.Marshal(hr)
}
