package intel

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/golang/glog"
	dpll "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

type ClockChainType int
type ClockChain struct {
	Type       ClockChainType  `json:"clockChainType"`
	LeadingNIC CardInfo        `json:"leadingNIC"`
	DpllPins   []*dpll.PinInfo `json:"dpllPins"`
}
type CardInfo struct {
	Name        string `json:"name"`
	DpllClockID string `json:"dpllClockId"`
	// upstreamPort specifies the slave port in the T-BC case. For example, if the "name"
	// 	is ens4f0, the "upstreamPort" could be ens4f1, depending on ptp4l config
	UpstreamPort string                  `json:"upstreamPort"`
	Pins         map[string]dpll.PinInfo `json:"pins"`
}

const (
	ClockTypeUnset ClockChainType = iota
	ClockTypeTGM
	ClockTypeTBC
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
)

type PinParentControl struct {
	EecPriority    uint8
	PpsPriority    uint8
	EecOutputState uint8
	PpsOutputState uint8
}
type PinControl struct {
	Label         string
	ParentControl PinParentControl
}

var configurablePins = []string{sdp20, sdp21, sdp22, sdp23, gnss, sma1Input, sma2Input, c8270Rclka, c8270Rclkb}

func (ch *ClockChain) GetLiveDpllPinsInfo() error {
	if !unitTest {
		conn, err := dpll.Dial(nil)
		if err != nil {
			return fmt.Errorf("failed to dial DPLL: %v", err)
		}
		//nolint:errcheck
		defer conn.Close()
		ch.DpllPins, err = conn.DumpPinGet()
		if err != nil {
			return fmt.Errorf("failed to dump DPLL pins: %v", err)
		}
	} else {
		ch.DpllPins = DpllPins
	}
	return nil
}

func (ch *ClockChain) ResolveInterconnections(e810Opts E810Opts, nodeProfile *ptpv1.PtpProfile) (*[]delayCompensation, error) {
	compensations := []delayCompensation{}
	var clockID *string
	for _, card := range e810Opts.PhaseInputs {
		delays, err := InitInternalDelays(card.Part)
		if err != nil {
			return nil, err
		}
		if card.Input != nil {

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
				clockID:   *clockID,
			})
		} else {
			ch.LeadingNIC.Name = card.ID
			ch.LeadingNIC.UpstreamPort = card.UpstreamPort
			clockID, err = addClockID(card.ID, nodeProfile)
			if err != nil {
				return nil, err
			}
			ch.LeadingNIC.DpllClockID = *clockID
			if card.GnssInput {
				ch.Type = ClockTypeTGM
				gnssLink := &delays.GnssInput
				compensations = append(compensations, delayCompensation{
					DelayPs:   gnssLink.DelayPs,
					pinLabel:  gnssLink.Pin,
					iface:     card.ID,
					direction: "input",
					clockID:   *clockID,
				})
			} else {
				// if no GNSS and no external, then ptp4l input
				ch.Type = ClockTypeTBC
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
				clockID:   *clockID,
			})
		}
	}
	return &compensations, nil
}

func InitClockChain(e810Opts E810Opts, nodeProfile *ptpv1.PtpProfile) (*ClockChain, error) {
	var chain = &ClockChain{
		LeadingNIC: CardInfo{
			Pins: make(map[string]dpll.PinInfo, 0),
		},
	}

	err := chain.GetLiveDpllPinsInfo()
	if err != nil {
		return chain, err
	}
	comps, err := chain.ResolveInterconnections(e810Opts, nodeProfile)
	if err != nil {
		glog.Errorf("fail to get delay compensations, %s", err)
	}
	if !unitTest {
		err = sendDelayCompensation(comps, chain.DpllPins)
		if err != nil {
			glog.Errorf("fail to send delay compensations, %s", err)
		}
	}
	err = chain.GetLeadingCardSDP()
	if err != nil {
		return chain, err
	}
	glog.Info("about to set DPLL pin defaults")
	_, err = chain.SetPinDefaults()
	if err != nil {
		return chain, err
	}
	if chain.Type == ClockTypeTBC {
		(*nodeProfile).PtpSettings["clockType"] = "T-BC"
		glog.Info("about to init TBC pins")
		_, err = chain.InitPinsTBC()
		if err != nil {
			return chain, fmt.Errorf("failed to initialize pins for T-BC operation: %s", err.Error())
		}
		glog.Info("about to enter TBC Normal mode")
		_, err = chain.EnterNormalTBC()
		if err != nil {
			return chain, fmt.Errorf("failed to enter T-BC normal mode: %s", err.Error())
		}
	}
	return chain, err
}

func (ch *ClockChain) GetLeadingCardSDP() error {
	clockID, err := strconv.ParseUint(ch.LeadingNIC.DpllClockID, 10, 64)
	if err != nil {
		return err
	}
	for _, pin := range ch.DpllPins {
		if pin.ClockID == clockID && slices.Contains(configurablePins, pin.BoardLabel) {
			ch.LeadingNIC.Pins[pin.BoardLabel] = *pin
		}
	}
	return nil
}

func writeSysFs(path string, val string) error {
	glog.Infof("writing " + val + " to " + path)
	err := os.WriteFile(path, []byte(val), 0666)
	if err != nil {
		return fmt.Errorf("e810 failed to write " + val + " to " + path + ": " + err.Error())
	}
	return nil
}

func (c *ClockChain) SetPinsControl(pins []PinControl) (*[]dpll.PinParentDeviceCtl, error) {
	pinCommands := []dpll.PinParentDeviceCtl{}
	for _, pinCtl := range pins {
		dpllPin, found := c.LeadingNIC.Pins[pinCtl.Label]
		if !found {
			return nil, fmt.Errorf("%s pin not found in the leading card", pinCtl.Label)
		}
		pinCommand := SetPinControlData(dpllPin, pinCtl.ParentControl)
		pinCommands = append(pinCommands, *pinCommand)
	}
	return &pinCommands, nil
}

func SetPinControlData(pin dpll.PinInfo, control PinParentControl) *dpll.PinParentDeviceCtl {
	Pin := dpll.PinParentDeviceCtl{
		ID:           pin.ID,
		PinParentCtl: make([]dpll.PinControl, 0),
	}

	for deviceIndex, parentDevice := range pin.ParentDevice {
		var prio uint32
		var outputState uint32
		pc := dpll.PinControl{}
		pc.PinParentID = parentDevice.ParentID
		switch deviceIndex {
		case eecDpllIndex:
			prio = uint32(control.EecPriority)
			outputState = uint32(control.EecOutputState)
		case ppsDpllIndex:
			prio = uint32(control.PpsPriority)
			outputState = uint32(control.PpsOutputState)
		}
		if parentDevice.Direction == dpll.PinDirectionInput {
			pc.Prio = &prio
		} else {
			pc.State = &outputState
		}
		Pin.PinParentCtl = append(Pin.PinParentCtl, pc)
	}
	return &Pin
}

func (c *ClockChain) EnableE810Outputs() error {
	// # echo 2 0 0 1 0 > /sys/class/net/$ETH/device/ptp/ptp*/period
	var pinPath string
	if unitTest {
		glog.Info("skip pin config in unit test")
		return nil
	} else {
		deviceDir := fmt.Sprintf("/sys/class/net/%s/device/ptp/", c.LeadingNIC.Name)
		phcs, err := os.ReadDir(deviceDir)
		if err != nil {
			return fmt.Errorf("e810 failed to read " + deviceDir + ": " + err.Error())
		}
		for _, phc := range phcs {
			pinPath = fmt.Sprintf("/sys/class/net/%s/device/ptp/%s/period", c.LeadingNIC.Name, phc.Name())
			err := writeSysFs(pinPath, sdp22PpsEnable)
			if err != nil {
				return fmt.Errorf("failed to write " + sdp22PpsEnable + " to " + pinPath + ": " + err.Error())
			}
		}
	}
	return nil
}

// InitPinsTBC initializes the leading card E810 and DPLL pins for T-BC operation
func (c *ClockChain) InitPinsTBC() (*[]dpll.PinParentDeviceCtl, error) {
	// Enable 1PPS output on SDP22
	// (To synchronize the DPLL1 to the E810 PHC synced by ptp4l):
	err := c.EnableE810Outputs()
	if err != nil {
		return nil, err
	}
	// Disable GNSS-1PPS, SDP20 and SDP21
	commands, err := c.SetPinsControl([]PinControl{
		{
			Label: gnss,
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
	})
	if err != nil {
		return nil, err
	}
	return commands, BatchPinSet(commands)
}

// EnterHoldoverTBC configures the leading card DPLL pins for T-BC holdover
func (c *ClockChain) EnterHoldoverTBC() (*[]dpll.PinParentDeviceCtl, error) {
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
		return nil, err
	}
	return commands, BatchPinSet(commands)
}

// EnterNormalTBC configures the leading card DPLL pins for regular T-BC operation
func (ch *ClockChain) EnterNormalTBC() (*[]dpll.PinParentDeviceCtl, error) {
	// Disable DPLL Outputs to e810 (SDP23, SDP21)
	// Enable DPLL inputs from e810 (SDP22)
	commands, err := ch.SetPinsControl([]PinControl{
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
		return nil, err
	}
	return commands, BatchPinSet(commands)
}

// SetPinDefaults initializes DPLL pins to default recommended values
func (ch *ClockChain) SetPinDefaults() (*[]dpll.PinParentDeviceCtl, error) {
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

	// Also, Enable DPLL Outputs to e810 (SDP21, SDP23)
	commands, err := ch.SetPinsControl([]PinControl{
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
				EecPriority: 3,
				PpsPriority: 3,
			},
		},
		{
			Label: sma2Input,
			ParentControl: PinParentControl{
				EecPriority: 2,
				PpsPriority: 2,
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
		return nil, err
	}
	return commands, BatchPinSet(commands)
}

func BatchPinSet(commands *[]dpll.PinParentDeviceCtl) error {
	if unitTest {
		return nil
	}
	conn, err := dpll.Dial(nil)
	if err != nil {
		return fmt.Errorf("failed to dial DPLL: %v", err)
	}
	//nolint:errcheck
	defer conn.Close()
	for _, command := range *commands {
		glog.Infof("DPLL pin command %++v", command)
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
