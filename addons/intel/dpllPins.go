package intel

import (
	"errors"
	"fmt"
	"strings"

	"github.com/golang/glog"
	dpll "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
)

// DPLLPins abstracts DPLL pin operations for mocking in tests.
type DPLLPins interface {
	ApplyPinCommands(commands []dpll.PinParentDeviceCtl) error
	FetchPins() error
	GetByLabel(label string, clockID uint64) *dpll.PinInfo
	GetAllPinsByLabel(label string) []*dpll.PinInfo
	GetCommandsForPluginPinSet(clockID uint64, pinset pinSet) []dpll.PinParentDeviceCtl
}

type dpllPins []*dpll.PinInfo

// DpllPins is the package-level DPLL pin accessor, replaceable for testing.
var DpllPins DPLLPins = &dpllPins{}

func (d *dpllPins) FetchPins() error {
	var pins []*dpll.PinInfo

	conn, err := dpll.Dial(nil)
	if err != nil {
		return fmt.Errorf("failed to dial DPLL: %v", err)
	}
	//nolint:errcheck
	defer conn.Close()
	pins, err = conn.DumpPinGet()
	if err != nil {
		return fmt.Errorf("failed to dump DPLL pins: %v", err)
	}
	*d = dpllPins(pins)
	return nil
}

func (d *dpllPins) GetByLabel(label string, clockID uint64) *dpll.PinInfo {
	for _, pin := range *d {
		if pin.BoardLabel == label && pin.ClockID == clockID {
			return pin
		}
	}
	return nil
}

func (d *dpllPins) GetAllPinsByLabel(label string) []*dpll.PinInfo {
	result := make([]*dpll.PinInfo, 0)

	for _, pin := range *d {
		if pin.BoardLabel == label {
			result = append(result, pin)
		}
	}
	return result
}

func (d *dpllPins) GetCommandsForPluginPinSet(clockID uint64, pinset pinSet) []dpll.PinParentDeviceCtl {
	pinCommands := make([]dpll.PinParentDeviceCtl, 0)

	for label, valueStr := range pinset {
		// TODO: Move label checks to a higher level function
		// if label == "U.FL1" || label == "U.FL2" {
		// 	glog.Warningf("%s can not longer be set via the pins on the plugin; values ignored", label)
		// 	continue
		// }

		valueStr = strings.TrimSpace(valueStr)
		values := strings.Fields(valueStr)
		if len(values) != 2 {
			glog.Errorf("Failed to unpack values for pin %s of clockID %d from '%s'", label, clockID, valueStr)
			continue
		}

		pinInfo := d.GetByLabel(label, clockID)
		if pinInfo == nil {
			glog.Errorf("not found pin with label %s for clockID %d", label, clockID)
			continue
		}
		if len(pinInfo.ParentDevice) == 0 {
			glog.Errorf("Unable to configure: No parent devices for pin %s for clockID %s", label, clockID)
			continue
		}

		ppCtrl := PinParentControl{}

		// For now we are ignoring the second value and setting both parent devices the same.
		for i, parentDev := range pinInfo.ParentDevice {
			var state uint8
			var priority uint8
			var direction *uint8

			// TODO add checks for capabilies such as direction changes.
			switch values[0] {
			case "0":
				if parentDev.Direction == dpll.PinDirectionInput {
					state = dpll.PinStateSelectable
				} else {
					state = dpll.PinStateDisconnected
				}
				priority = PriorityDisabled
			case "1":
				state = dpll.PinStateConnected
				v := uint8(dpll.PinDirectionInput)
				direction = &v
				priority = PriorityEnabled
			case "2":
				state = dpll.PinStateConnected
				v := uint8(dpll.PinDirectionOutput)
				direction = &v
				priority = PriorityEnabled
			default:
				glog.Errorf("invalid initial value in pin config for clock id %s pin %s: '%s'", clockID, label, values[0])
				continue
			}

			switch i {
			case eecDpllIndex:
				ppCtrl.EecOutputState = state
				ppCtrl.EecDirection = direction
				ppCtrl.EecPriority = priority
			case ppsDpllIndex:
				ppCtrl.PpsOutputState = state
				ppCtrl.PpsDirection = direction
				ppCtrl.PpsPriority = priority
			}
		}
		pinCommands = append(pinCommands, SetPinControlData(*pinInfo, ppCtrl)...)
	}
	return pinCommands
}

func (d *dpllPins) ApplyPinCommands(commands []dpll.PinParentDeviceCtl) error {
	err := BatchPinSet(commands)
	// event if there was an error we still need to refresh the pin state.
	fetchErr := d.FetchPins()
	return errors.Join(err, fetchErr)
}
