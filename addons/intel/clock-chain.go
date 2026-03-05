package intel

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang/glog"
	dpll "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

type (
	// ClockChainType represents the type of clock chain
	ClockChainType int

	// ClockChain represents a set of interrelated clocks
	ClockChain struct {
		Type       ClockChainType `json:"clockChainType"`
		LeadingNIC CardInfo       `json:"LeadingNIC"`
		OtherNICs  []CardInfo     `json:"otherNICs,omitempty"`
		DpllPins   DPLLPins       `json:"dpllPins"`
	}

	// ClockChainInterface is the mockable public interface of the ClockChain
	ClockChainInterface interface {
		EnterNormalTBC() error
		EnterHoldoverTBC() error
		SetPinDefaults() error
		GetLeadingNIC() CardInfo
	}

	// CardInfo represents an individual card in the clock chain
	CardInfo struct {
		Name        string `json:"name"`
		DpllClockID uint64 `json:"dpllClockId"`
		// upstreamPort specifies the slave port in the T-BC case. For example, if the "name"
		// 	is ens4f0, the "upstreamPort" could be ens4f1, depending on ptp4l config
		UpstreamPort string `json:"upstreamPort"`
		// Pins         map[string]dpll.PinInfo `json:"pins"`
	}

	// PhaseInputsProvider abstracts access to PhaseInputs so InitClockChain can
	// accept different option structs (e.g., E810Opts, E825Opts)
	PhaseInputsProvider interface {
		GetPhaseInputs() []PhaseInputs
	}
)

const (
	ClockTypeUnset ClockChainType = iota
	ClockTypeTGM
	ClockTypeTBC
)
const (
	PriorityEnabled  = 0
	PriorityDisabled = 255
)

var ClockTypesMap = map[string]ClockChainType{
	"":     ClockTypeUnset,
	"T-GM": ClockTypeTGM,
	"T-BC": ClockTypeTBC, // Use the same for T-TSC
}

const (
	sdp20          = "CVL-SDP20"
	sdp21          = "CVL-SDP21"
	sdp22          = "CVL-SDP22"
	sdp23          = "CVL-SDP23"
	gnss           = "GNSS-1PPS"
	sma1Input      = "SMA1"
	sma2Input      = "SMA2/U.FL2"
	c8270Rclka     = "C827_0-RCLKA"
	c8270Rclkb     = "C827_0-RCLKB"
	eecDpllIndex   = 0
	ppsDpllIndex   = 1
	sdp22PpsEnable = "2 0 0 1 0"
	sdp20PpsEnable = "1 0 0 1 0"
	sma2           = "SMA2"
)

type PinParentControl struct {
	EecPriority    uint8
	PpsPriority    uint8
	EecOutputState uint8
	PpsOutputState uint8
	EecDirection   *uint8
	PpsDirection   *uint8
}
type PinControl struct {
	Label         string
	ParentControl PinParentControl
}

// GetLeadingNIC returns the leading NIC from the clock chain
func (c *ClockChain) GetLeadingNIC() CardInfo {
	return c.LeadingNIC
}

func (c *ClockChain) resolveInterconnections(opts PhaseInputsProvider, nodeProfile *ptpv1.PtpProfile) (*[]delayCompensation, error) {
	compensations := []delayCompensation{}
	for _, card := range opts.GetPhaseInputs() {
		delays, err := InitInternalDelays(card.Part)
		if err != nil {
			return nil, err
		}
		glog.Infof("card: %+v", card)
		var clockID uint64

		if !card.GnssInput && card.UpstreamPort == "" {
			externalDelay := card.Input.DelayPs
			connector := card.Input.Connector
			link := findInternalLink(delays.ExternalInputs, connector)
			if link == nil {
				return nil, fmt.Errorf("plugin E810 error: can't find connector %s in the card %s spec", connector, card.Part)
			}
			var pinLabel string
			var internalDelay int32

			pinLabel = link.Pin
			internalDelay = link.DelayPs
			clockID, err = addClockID(card.ID, nodeProfile)
			if err != nil {
				return nil, err
			}
			compensations = append(compensations, delayCompensation{
				DelayPs:   int32(externalDelay) + internalDelay,
				pinLabel:  pinLabel,
				iface:     card.ID,
				direction: "input",
				clockID:   clockID,
			})
			// Track non-leading NICs
			c.OtherNICs = append(c.OtherNICs, CardInfo{
				Name:         card.ID,
				DpllClockID:  clockID,
				UpstreamPort: card.UpstreamPort,
			})
		} else {
			c.LeadingNIC.Name = card.ID
			c.LeadingNIC.UpstreamPort = card.UpstreamPort
			c.LeadingNIC.DpllClockID, err = addClockID(card.ID, nodeProfile)
			if err != nil {
				return nil, err
			}
			if card.GnssInput {
				c.Type = ClockTypeTGM
				gnssLink := &delays.GnssInput
				compensations = append(compensations, delayCompensation{
					DelayPs:   gnssLink.DelayPs,
					pinLabel:  gnssLink.Pin,
					iface:     card.ID,
					direction: "input",
					clockID:   c.LeadingNIC.DpllClockID,
				})
			} else {
				// if no GNSS and no external, then ptp4l input
				c.Type = ClockTypeTBC
			}
		}
		for _, outputConn := range card.PhaseOutputConnectors {
			link := findInternalLink(delays.ExternalOutputs, outputConn)
			if link == nil {
				return nil, fmt.Errorf("plugin E810 error: can't find connector %s in the card %s spec", outputConn, card.Part)
			}
			clockID, err = addClockID(card.ID, nodeProfile)
			if err != nil {
				return nil, err
			}
			compensations = append(compensations, delayCompensation{
				DelayPs:   link.DelayPs,
				pinLabel:  link.Pin,
				iface:     card.ID,
				direction: "output",
				clockID:   clockID,
			})
		}
	}
	return &compensations, nil
}

// InitClockChain initializes the ClockChain struct based on live DPLL pin info
func InitClockChain(opts PhaseInputsProvider, nodeProfile *ptpv1.PtpProfile) (*ClockChain, error) {
	chain := &ClockChain{
		LeadingNIC: CardInfo{},
		OtherNICs:  make([]CardInfo, 0),
		DpllPins:   DpllPins,
	}

	err := chain.DpllPins.FetchPins()
	if err != nil {
		return chain, err
	}

	comps, err := chain.resolveInterconnections(opts, nodeProfile)
	if err != nil {
		glog.Errorf("fail to get delay compensations, %s", err)
	}
	err = SendDelayCompensation(comps, chain.DpllPins)
	if err != nil {
		glog.Errorf("fail to send delay compensations, %s", err)
	}
	glog.Info("about to set DPLL pin priorities to defaults")
	err = chain.SetPinDefaults()
	if err != nil {
		return chain, err
	}
	if chain.Type == ClockTypeTBC {
		(*nodeProfile).PtpSettings["clockType"] = "T-BC"
		glog.Info("about to init TBC pins")
		err = chain.InitPinsTBC()
		if err != nil {
			return chain, fmt.Errorf("failed to initialize pins for T-BC operation: %s", err.Error())
		}
	}
	return chain, err
}

func writeSysFs(path string, val string) error {
	glog.Infof("writing " + val + " to " + path)
	err := filesystem.WriteFile(path, []byte(val), 0o666)
	if err != nil {
		return fmt.Errorf("e810 failed to write "+val+" to "+path+": %v", err.Error())
	}
	return nil
}

// SetPinsControl builds DPLL netlink commands for the given pins on the leading NIC.
func (c *ClockChain) SetPinsControl(pins []PinControl) ([]dpll.PinParentDeviceCtl, error) {
	pinCommands := []dpll.PinParentDeviceCtl{}
	for _, pinCtl := range pins {
		dpllPin := c.DpllPins.GetByLabel(pinCtl.Label, c.LeadingNIC.DpllClockID)
		if dpllPin == nil {
			glog.Errorf("pin not found with label %s for clockID %d", pinCtl.Label, c.LeadingNIC.DpllClockID)
			continue
		}
		pinCommands = append(pinCommands, SetPinControlData(*dpllPin, pinCtl.ParentControl)...)
	}
	return pinCommands, nil
}

// SetPinsControlForAllNICs sets pins across all NICs (leading + other NICs)
// This is used specifically for initialization functions like SetPinDefaults
func (c *ClockChain) SetPinsControlForAllNICs(pins []PinControl) ([]dpll.PinParentDeviceCtl, error) {
	pinCommands := []dpll.PinParentDeviceCtl{}
	errs := make([]error, 0)

	for _, pinCtl := range pins {
		foundPins := c.DpllPins.GetAllPinsByLabel(pinCtl.Label)
		if len(foundPins) == 0 && pinCtl.Label == sma2Input {
			pinCtl.Label = sma2
			foundPins = c.DpllPins.GetAllPinsByLabel(pinCtl.Label)
		}
		if len(foundPins) == 0 {
			errs = append(errs, fmt.Errorf("pin %s not found on any nic", pinCtl.Label))
			continue
		}
		for _, pin := range foundPins {
			pinCommands = append(pinCommands, SetPinControlData(*pin, pinCtl.ParentControl)...)
		}
	}

	return pinCommands, errors.Join(errs...)
}

// buildDirectionCmd checks if any parent device direction is changing.
// If so, it returns a direction-only command and updates pin.ParentDevice
// directions in place. Returns nil if no direction change is needed.
// The kernel rejects combining direction changes with prio/state.
func buildDirectionCmd(pin *dpll.PinInfo, control PinParentControl) *dpll.PinParentDeviceCtl {
	var cmd *dpll.PinParentDeviceCtl

	for i, parentDevice := range pin.ParentDevice {
		var direction *uint32
		switch i {
		case eecDpllIndex:
			if control.EecDirection != nil {
				v := uint32(*control.EecDirection)
				direction = &v
			}
		case ppsDpllIndex:
			if control.PpsDirection != nil {
				v := uint32(*control.PpsDirection)
				direction = &v
			}
		}
		if direction != nil && *direction != parentDevice.Direction && pin.Capabilities&dpll.PinCapDir != 0 {
			if cmd == nil {
				cmd = &dpll.PinParentDeviceCtl{ID: pin.ID}
			}
			cmd.PinParentCtl = append(cmd.PinParentCtl,
				dpll.PinControl{PinParentID: parentDevice.ParentID, Direction: direction})
			pin.ParentDevice[i].Direction = *direction
		}
	}
	return cmd
}

// SetPinControlData builds DPLL netlink commands to configure a pin's parent devices.
func SetPinControlData(pin dpll.PinInfo, control PinParentControl) []dpll.PinParentDeviceCtl {
	dirCmd := buildDirectionCmd(&pin, control)

	cmd := dpll.PinParentDeviceCtl{ID: pin.ID}
	for i, parentDevice := range pin.ParentDevice {
		var prio, outputState uint32

		switch i {
		case eecDpllIndex:
			prio = uint32(control.EecPriority)
			outputState = uint32(control.EecOutputState)
		case ppsDpllIndex:
			prio = uint32(control.PpsPriority)
			outputState = uint32(control.PpsOutputState)
		}

		pc := dpll.PinControl{PinParentID: parentDevice.ParentID}
		if parentDevice.Direction == dpll.PinDirectionInput {
			if pin.Capabilities&dpll.PinCapState != 0 {
				selectable := uint32(dpll.PinStateSelectable)
				pc.State = &selectable
			}
			if parentDevice.Prio != nil && pin.Capabilities&dpll.PinCapPrio != 0 {
				pc.Prio = &prio
			}
		} else if pin.Capabilities&dpll.PinCapState != 0 {
			pc.State = &outputState
		}
		cmd.PinParentCtl = append(cmd.PinParentCtl, pc)
	}

	if dirCmd != nil {
		return []dpll.PinParentDeviceCtl{*dirCmd, cmd}
	}
	return []dpll.PinParentDeviceCtl{cmd}
}

func (c *ClockChain) EnableE810Outputs() error {
	// # echo 2 2 > /sys/class/net/$ETH/device/ptp/ptp*/pins/SMA2
	// # echo 2 0 0 1 0 > /sys/class/net/$ETH/device/ptp/ptp*/period
	var pinPath string

	deviceDir := fmt.Sprintf("/sys/class/net/%s/device/ptp/", c.LeadingNIC.Name)
	phcs, err := filesystem.ReadDir(deviceDir)
	if err != nil {
		return fmt.Errorf("e810 failed to read "+deviceDir+": %v", err.Error())
	}
	if len(phcs) > 1 {
		glog.Error("e810 cards should have one PHC per NIC, but %s has %d",
			c.LeadingNIC.Name, len(phcs))
	}
	if len(phcs) == 0 {
		e := fmt.Sprintf("e810 cards should have one PHC per NIC, but %s has 0",
			c.LeadingNIC.Name)
		glog.Error(e)
		return errors.New(e)
	}
	if hasSysfsSMAPins(c.LeadingNIC.Name) {
		err = pinConfig.applyPinSet(c.LeadingNIC.Name, pinSet{"SMA2": "2 2"})
		if err != nil {
			glog.Errorf("failed to set SMA2 pin via sysfs: %s", err)
		}
	} else {
		sma2Cmds := c.DpllPins.GetCommandsForPluginPinSet(c.LeadingNIC.DpllClockID, map[string]string{"SMA2": "2 2"})
		err = c.DpllPins.ApplyPinCommands(sma2Cmds)
		if err != nil {
			glog.Errorf("failed to set SMA2 pin to output: %s", err)
		}
	}

	pinPath = fmt.Sprintf("%s%s/period", deviceDir, phcs[0].Name())
	err = writeSysFs(pinPath, sdp22PpsEnable)
	if err != nil {
		glog.Errorf("failed to write " + sdp22PpsEnable + " to " + pinPath + ": " + err.Error())
	}

	return nil
}

// InitPinsTBC initializes the leading card E810 and DPLL pins for T-BC operation
func (c *ClockChain) InitPinsTBC() error {
	// Enable 1PPS output on SDP22
	// (To synchronize the DPLL1 to the E810 PHC synced by ptp4l):
	err := c.EnableE810Outputs()
	if err != nil {
		return fmt.Errorf("failed to enable E810 outputs: %w", err)
	}
	// Disable GNSS-1PPS (all cards), SDP20 and SDP21
	commandsGnss, err := c.SetPinsControlForAllNICs([]PinControl{
		{
			Label: gnss,
			ParentControl: PinParentControl{
				EecPriority: PriorityDisabled,
				PpsPriority: PriorityDisabled,
			},
		},
	})
	if err != nil {
		glog.Error("failed to disable GNSS: ", err)
	}
	commands, err := c.SetPinsControl([]PinControl{
		{
			Label: sdp22,
			ParentControl: PinParentControl{
				EecPriority: PriorityDisabled,
				PpsPriority: PriorityDisabled,
			},
		},
		{
			Label: sdp20,
			ParentControl: PinParentControl{
				EecPriority: PriorityDisabled,
				PpsPriority: PriorityDisabled,
			},
		},
		{
			Label: sdp21,
			ParentControl: PinParentControl{
				EecOutputState: dpll.PinStateDisconnected,
				PpsOutputState: dpll.PinStateDisconnected,
			},
		},
		{
			Label: sdp23,
			ParentControl: PinParentControl{
				EecOutputState: dpll.PinStateDisconnected,
				PpsOutputState: dpll.PinStateDisconnected,
			},
		},
	})
	if err != nil {
		glog.Error("failed to set pins control: ", err)
	}
	commands = append(commands, commandsGnss...)

	err = BatchPinSet(commands)
	// event if there was an error we still need to refresh the pin state.
	fetchErr := c.DpllPins.FetchPins()
	return errors.Join(err, fetchErr)
}

// EnterHoldoverTBC configures the leading card DPLL pins for T-BC holdover
func (c *ClockChain) EnterHoldoverTBC() error {
	// Disable DPLL inputs from e810 (SDP22)
	// Enable DPLL Outputs to e810 (SDP21, SDP23)
	commands, err := c.SetPinsControl([]PinControl{
		{
			Label: sdp22,
			ParentControl: PinParentControl{
				EecPriority: PriorityDisabled,
				PpsPriority: PriorityDisabled,
			},
		},
		{
			Label: sdp23,
			ParentControl: PinParentControl{
				EecOutputState: dpll.PinStateConnected,
				PpsOutputState: dpll.PinStateConnected,
			},
		},
	})
	if err != nil {
		return err
	}
	err = BatchPinSet(commands)
	// event if there was an error we still need to refresh the pin state.
	fetchErr := c.DpllPins.FetchPins()
	return errors.Join(err, fetchErr)
}

// EnterNormalTBC configures the leading card DPLL pins for regular T-BC operation
func (c *ClockChain) EnterNormalTBC() error {
	// Disable DPLL Outputs to e810 (SDP23, SDP21)
	// Enable DPLL inputs from e810 (SDP22)
	commands, err := c.SetPinsControl([]PinControl{
		{
			Label: sdp22,
			ParentControl: PinParentControl{
				EecPriority: PriorityDisabled,
				PpsPriority: PriorityEnabled,
			},
		},
		{
			Label: sdp23,
			ParentControl: PinParentControl{
				EecOutputState: dpll.PinStateDisconnected,
				PpsOutputState: dpll.PinStateDisconnected,
			},
		},
	})
	if err != nil {
		return err
	}
	err = BatchPinSet(commands)
	// event if there was an error we still need to refresh the pin state.
	fetchErr := c.DpllPins.FetchPins()
	return errors.Join(err, fetchErr)
}

// SetPinDefaults initializes DPLL pins to default recommended values
func (c *ClockChain) SetPinDefaults() error {
	// DPLL Priority List:
	//
	//	Recommended | Pin Index | EEC-DPLL0                    | PPS-DPLL1
	//	Priority    |           | (Frequency/Glitchless)       | (Phase/Glitch Allowed)
	//	------------|-----------|------------------------------|-----------------------------
	//	0           | 6         | 1PPS from GNSS (GNSS-1PPS)  | 1PPS from GNSS (GNSS-1PPS)
	//	2           | 5         | 1PPS from SMA2 (SMA2)       | 1PPS from SMA2 (SMA2)
	//	3           | 4         | 1PPS from SMA1 (SMA1)       | 1PPS from SMA1 (SMA1)
	//	4           | 1         | Reserved                     | 1PPS from E810 (CVL-SDP20)
	//	5           | 0         | Reserved                     | 1PPS from E810 (CVL-SDP22)
	//	6           | --        | Reserved                     | Reserved
	//	7           | --        | Reserved                     | Reserved
	//	8           | 2         | Recovered CLK1 (C827_0-RCLKA) | Recovered CLK1 (C827_0-RCLKA)
	//	9           | 3         | Recovered CLK2 (C827_0-RCLKB) | Recovered CLK2 (C827_0-RCLKB)
	//	10          | --        | OCXO                         | OCXO
	d := uint8(dpll.PinDirectionInput)
	// Also, Enable DPLL Outputs to e810 (SDP21, SDP23)
	commands, err := c.SetPinsControlForAllNICs([]PinControl{
		{
			Label: gnss,
			ParentControl: PinParentControl{
				EecPriority: 0,
				PpsPriority: 0,
			},
		},
		{
			Label: sma1Input,
			ParentControl: PinParentControl{
				EecPriority:  3,
				PpsPriority:  3,
				EecDirection: &d,
				PpsDirection: &d,
			},
		},
		{
			Label: sma2Input,
			ParentControl: PinParentControl{
				EecPriority:  2,
				PpsPriority:  2,
				EecDirection: &d,
				PpsDirection: &d,
			},
		},
		{
			Label: sdp20,
			ParentControl: PinParentControl{
				EecPriority: PriorityDisabled,
				PpsPriority: 4,
			},
		},
		{
			Label: sdp22,
			ParentControl: PinParentControl{
				EecPriority: PriorityDisabled,
				PpsPriority: 5,
			},
		},
		{
			Label: sdp21,
			ParentControl: PinParentControl{
				EecOutputState: dpll.PinStateDisconnected,
				PpsOutputState: dpll.PinStateConnected,
			},
		},
		{
			Label: sdp23,
			ParentControl: PinParentControl{
				EecOutputState: dpll.PinStateDisconnected,
				PpsOutputState: dpll.PinStateConnected,
			},
		},
		{
			Label: c8270Rclka,
			ParentControl: PinParentControl{
				EecPriority: 8,
				PpsPriority: 8,
			},
		},
		{
			Label: c8270Rclkb,
			ParentControl: PinParentControl{
				EecPriority: 9,
				PpsPriority: 9,
			},
		},
	})
	if err != nil {
		return err
	}
	err = BatchPinSet(commands)
	// event if there was an error we still need to refresh the pin state.
	fetchErr := c.DpllPins.FetchPins()
	return errors.Join(err, fetchErr)
}

// BatchPinSet function pointer allows mocking of BatchPinSet
var BatchPinSet = batchPinSet

func batchPinSet(commands []dpll.PinParentDeviceCtl) error {
	conn, err := dpll.Dial(nil)
	if err != nil {
		return fmt.Errorf("failed to dial DPLL: %v", err)
	}
	//nolint:errcheck
	defer conn.Close()
	for _, command := range commands {
		glog.Infof("DPLL pin command %#v", command)
		b, err := dpll.EncodePinControl(command)
		if err != nil {
			return err
		}
		err = conn.SendCommand(dpll.DpllCmdPinSet, b)
		if err != nil {
			glog.Error("failed to send pin command: ", err)
			return err
		}
		info, err := conn.DoPinGet(dpll.DoPinGetRequest{ID: command.ID})
		if err != nil {
			glog.Error("failed to get pin: ", err)
			return err
		}
		reply, err := dpll.GetPinInfoHR(info, time.Now())
		if err != nil {
			glog.Error("failed to convert pin reply to human readable: ", err)
			return err
		}
		glog.Info("pin reply: ", string(reply))
	}
	return nil
}
