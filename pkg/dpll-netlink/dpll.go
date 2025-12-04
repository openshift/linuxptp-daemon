// Description: DPLL subsystem.

package dpll_netlink

import (
	"errors"
	"log"
	"math"

	"github.com/mdlayher/genetlink"
	"github.com/mdlayher/netlink"
)

// A Conn is a connection to netlink family "dpll".
type Conn struct {
	c *genetlink.Conn
	f genetlink.Family
}

// GetGenetlinkConn exposes genetlink connection
func (c *Conn) GetGenetlinkConn() *genetlink.Conn {
	return c.c
}

// GetGenetlinkFamily exposes genetlink family
func (c *Conn) GetGenetlinkFamily() genetlink.Family {
	return c.f
}

// GetMcastGroupID finds the requested multicast group in the family and returns its ID
func (c *Conn) GetMcastGroupID(mcGroup string) (id uint32, found bool) {
	for _, group := range c.f.Groups {
		if group.Name == mcGroup {
			return group.ID, true
		}
	}
	return 0, false
}

// Dial opens a Conn for netlink family "dpll". Any options are passed directly
// to the underlying netlink package.
func Dial(cfg *netlink.Config) (*Conn, error) {
	c, err := genetlink.Dial(cfg)
	if err != nil {
		return nil, err
	}

	f, err := c.GetFamily("dpll")
	if err != nil {
		return nil, err
	}

	return &Conn{c: c, f: f}, nil
}

// Close closes the Conn's underlying netlink connection.
func (c *Conn) Close() error { return c.c.Close() }

// DoDeviceIDGet wraps the "device-id-get" operation:
// Get id of dpll device that matches given attributes
func (c *Conn) DoDeviceIDGet(req DoDeviceIDGetRequest) (*DoDeviceIDGetReply, error) {
	ae := netlink.NewAttributeEncoder()
	if req.ClockID != 0 {
		ae.Uint64(DpllClockID, req.ClockID)
	}
	if req.Type != 0 {
		ae.Uint8(DpllType, req.Type)
	}

	b, err := ae.Encode()
	if err != nil {
		return nil, err
	}

	msg := genetlink.Message{
		Header: genetlink.Header{
			Command: DpllCmdDeviceIDGet,
			Version: c.f.Version,
		},
		Data: b,
	}

	msgs, err := c.c.Execute(msg, c.f.ID, netlink.Request)
	if err != nil {
		return nil, err
	}

	replies := make([]*DoDeviceIDGetReply, 0, len(msgs))
	for _, m := range msgs {
		ad, err := netlink.NewAttributeDecoder(m.Data)
		if err != nil {
			return nil, err
		}

		var reply DoDeviceIDGetReply
		for ad.Next() {
			switch ad.Type() {
			case DpllID:
				reply.ID = ad.Uint32()
			}
		}

		if err := ad.Err(); err != nil {
			return nil, err
		}

		replies = append(replies, &reply)
	}

	if len(replies) != 1 {
		return nil, errors.New("dpll: expected exactly one DoDeviceIDGetReply")
	}

	return replies[0], nil
}

// DoDeviceIDGetRequest is used with the DoDeviceIDGet method.
type DoDeviceIDGetRequest struct {
	// TODO: field "ModuleName", type "string"
	ClockID uint64
	Type    uint8
}

// DoDeviceIDGetReply is used with the DoDeviceIDGet method.
type DoDeviceIDGetReply struct {
	ID uint32
}

func ParseDeviceReplies(msgs []genetlink.Message) ([]*DoDeviceGetReply, error) {
	replies := make([]*DoDeviceGetReply, 0, len(msgs))
	for _, m := range msgs {
		ad, err := netlink.NewAttributeDecoder(m.Data)
		if err != nil {
			return nil, err
		}
		var reply DoDeviceGetReply
		for ad.Next() {
			switch ad.Type() {
			case DpllID:
				reply.ID = ad.Uint32()
			case DpllModuleName:
				reply.ModuleName = ad.String()
			case DpllMode:
				reply.Mode = ad.Uint32()
			case DpllModeSupported:
				reply.ModeSupported = append(reply.ModeSupported, ad.Uint32())
			case DpllLockStatus:
				reply.LockStatus = ad.Uint32()
			case DpllAttPadding:
			case DpllTemp:
				reply.Temp = ad.Int32()
			case DpllClockID:
				reply.ClockID = ad.Uint64()
			case DpllType:
				reply.Type = ad.Uint32()
			case DpllLockStatusError:
				reply.LockStatusError = ad.Uint32()
			case DpllClockQualityLevel:
				reply.ClockQualityLevel = append(reply.ClockQualityLevel, ad.Uint32())
			case DpllPhaseOffsetMonitor:
				reply.PhaseOffsetMonitor = ad.Uint32()
			case DpllPhaseOffsetAverageFactor:
				reply.PhaseOffsetAverageFactor = ad.Uint32()
			default:
				log.Println("default", ad.Type(), len(ad.Bytes()), ad.Bytes())
			}
		}
		if err := ad.Err(); err != nil {
			return nil, err
		}
		replies = append(replies, &reply)
	}
	return replies, nil
}

// DoDeviceGet wraps the "device-get" operation:
// Get list of DPLL devices (dump) or attributes of a single dpll device
func (c *Conn) DoDeviceGet(req DoDeviceGetRequest) (*DoDeviceGetReply, error) {
	ae := netlink.NewAttributeEncoder()
	if req.ID != 0 {
		ae.Uint32(DpllID, req.ID)
	}
	// TODO: field "req.ModuleName", type "string"

	b, err := ae.Encode()
	if err != nil {
		return nil, err
	}

	msg := genetlink.Message{
		Header: genetlink.Header{
			Command: DpllCmdDeviceGet,
			Version: c.f.Version,
		},
		Data: b,
	}

	msgs, err := c.c.Execute(msg, c.f.ID, netlink.Request)
	if err != nil {
		return nil, err
	}

	replies, err := ParseDeviceReplies(msgs)
	if err != nil {
		return nil, err
	}

	if len(replies) != 1 {
		return nil, errors.New("dpll: expected exactly one DoDeviceGetReply")
	}

	return replies[0], nil
}

// DumpDeviceGet wraps the "device-get" operation:
// Get list of DPLL devices (dump) or attributes of a single dpll device

func (c *Conn) DumpDeviceGet() ([]*DoDeviceGetReply, error) {
	// No attribute arguments.
	var b []byte

	msg := genetlink.Message{
		Header: genetlink.Header{
			Command: DpllCmdDeviceGet,
			Version: c.f.Version,
		},
		Data: b,
	}

	msgs, err := c.c.Execute(msg, c.f.ID, netlink.Request|netlink.Dump)
	if err != nil {
		return nil, err
	}

	replies, err := ParseDeviceReplies(msgs)
	if err != nil {
		return nil, err
	}
	return replies, nil
}

// DoDeviceGetRequest is used with the DoDeviceGet method.
type DoDeviceGetRequest struct {
	ID         uint32
	ModuleName string
}

// DoDeviceGetReply is used with the DoDeviceGet method.
type DoDeviceGetReply struct {
	ID                       uint32
	ModuleName               string
	Mode                     uint32
	ModeSupported            []uint32
	LockStatus               uint32
	Temp                     int32
	ClockID                  uint64
	Type                     uint32
	LockStatusError          uint32
	ClockQualityLevel        []uint32
	PhaseOffsetMonitor       uint32
	PhaseOffsetAverageFactor uint32
}

func ParsePinReplies(msgs []genetlink.Message) ([]*PinInfo, error) {
	replies := make([]*PinInfo, 0, len(msgs))

	for _, m := range msgs {
		ad, err := netlink.NewAttributeDecoder(m.Data)
		if err != nil {
			return nil, err
		}
		var reply PinInfo
		for ad.Next() {
			switch ad.Type() {
			case DpllPinID:
				reply.ID = ad.Uint32()
			case DpllPinParentID:
				reply.ParentID = ad.Uint32()
			case DpllPinModuleName:
				reply.ModuleName = ad.String()
			case DpllPinClockID:
				reply.ClockID = ad.Uint64()
			case DpllPinBoardLabel:
				reply.BoardLabel = ad.String()
			case DpllPinPanelLabel:
				reply.PanelLabel = ad.String()
			case DpllPinPackageLabel:
				reply.PackageLabel = ad.String()
			case DpllPinType:
				reply.Type = ad.Uint32()
			case DpllPinFrequency:
				reply.Frequency = ad.Uint64()
			case DpllPinFrequencySupported:
				ad.Nested(func(ad *netlink.AttributeDecoder) error {
					var temp FrequencyRange
					for ad.Next() {
						switch ad.Type() {
						case DpllPinFrequencyMin:
							temp.FrequencyMin = ad.Uint64()
						case DpllPinFrequencyMax:
							temp.FrequencyMax = ad.Uint64()
						}
					}
					reply.FrequencySupported = append(reply.FrequencySupported, temp)
					return nil
				})
			case DpllPinCapabilities:
				reply.Capabilities = ad.Uint32()
			case DpllPinParentDevice:
				ad.Nested(func(ad *netlink.AttributeDecoder) error {
					temp := PinParentDevice{
						// Initialize phase offset to a max value, so later we can detect it has been updated
						PhaseOffset: math.MaxInt64,
					}
					for ad.Next() {
						switch ad.Type() {
						case DpllPinParentID:
							temp.ParentID = ad.Uint32()
						case DpllPinDirection:
							temp.Direction = ad.Uint32()
						case DpllPinPrio:
							temp.Prio = ad.Uint32()
						case DpllPinState:
							temp.State = ad.Uint32()
						case DpllPinPhaseOffset:
							temp.PhaseOffset = ad.Int64()
						}

					}
					reply.ParentDevice = append(reply.ParentDevice, temp)
					return nil
				})
			case DpllPinParentPin:
				ad.Nested(func(ad *netlink.AttributeDecoder) error {
					var temp PinParentPin
					for ad.Next() {
						switch ad.Type() {
						case DpllPinParentID:
							temp.ParentID = ad.Uint32()
						case DpllPinState:
							temp.State = ad.Uint32()
						}
					}
					reply.ParentPin = append(reply.ParentPin, temp)
					return nil
				})
			case DpllPinPhaseAdjustMin:
				reply.PhaseAdjustMin = ad.Int32()
			case DpllPinPhaseAdjustMax:
				reply.PhaseAdjustMax = ad.Int32()
			case DpllPinPhaseAdjust:
				reply.PhaseAdjust = ad.Int32()
			case DpllPinPhaseOffset:
				reply.PhaseOffset = ad.Int64()
			case DpllPinFractionalFrequencyOffset:
				reply.FractionalFrequencyOffset = int(ad.Int32())
			case DpllPinEsyncFrequency:
				reply.EsyncFrequency = ad.Int64()
			case DpllPinEsyncFrequencySupported:
				ad.Nested(func(ad *netlink.AttributeDecoder) error {
					var temp FrequencyRange
					for ad.Next() {
						switch ad.Type() {
						case DpllPinFrequencyMin:
							temp.FrequencyMin = ad.Uint64()
						case DpllPinFrequencyMax:
							temp.FrequencyMax = ad.Uint64()
						}
					}
					reply.EsyncFrequencySupported = append(reply.EsyncFrequencySupported, temp)
					return nil
				})
			case DpllPinEsyncPulse:
				reply.EsyncPulse = ad.Uint32()
			case DpllPinReferenceSync:
				ad.Nested(func(ad *netlink.AttributeDecoder) error {
					var temp ReferenceSync
					for ad.Next() {
						switch ad.Type() {
						case DpllPinID:
							temp.ID = ad.Uint32()
						}
					}
					reply.ReferenceSync = append(reply.ReferenceSync, temp)
					return nil
				})
			case DpllPinPhaseAdjustGran:
				reply.PhaseAdjustGran = ad.Uint32()
			default:
				log.Printf("unrecognized type: %d\n", ad.Type())
			}
		}
		if err := ad.Err(); err != nil {
			return nil, err
		}
		replies = append(replies, &reply)
	}
	return replies, nil
}

// DoPinGet wraps the "pin-get" operation:
func (c *Conn) DoPinGet(req DoPinGetRequest) (*PinInfo, error) {
	ae := netlink.NewAttributeEncoder()
	ae.Uint32(DpllPinID, req.ID)

	b, err := ae.Encode()
	if err != nil {
		return nil, err
	}

	msg := genetlink.Message{
		Header: genetlink.Header{
			Command: DpllCmdPinGet,
			Version: c.f.Version,
		},
		Data: b,
	}
	msgs, err := c.c.Execute(msg, c.f.ID, netlink.Request)
	if err != nil {
		return nil, err
	}

	replies, err := ParsePinReplies(msgs)
	if err != nil {
		return nil, err
	}
	if len(replies) != 1 {
		return nil, errors.New("dpll: expected exactly one PinInfo")
	}

	return replies[0], nil
}

func (c *Conn) DumpPinGet() ([]*PinInfo, error) {
	msg := genetlink.Message{
		Header: genetlink.Header{
			Command: DpllCmdPinGet,
			Version: c.f.Version,
		},
	}

	msgs, err := c.c.Execute(msg, c.f.ID, netlink.Request|netlink.Dump)
	if err != nil {
		return nil, err
	}

	replies, err := ParsePinReplies(msgs)
	if err != nil {
		return nil, err
	}

	return replies, nil
}

// DoPinGetRequest is used with the DoPinGet method.
type DoPinGetRequest struct {
	ID uint32
}

// PinInfo is used with the DoPinSet /DoPinGet / DumpPinGet / monitor methods.
type PinInfo struct {
	ID                        uint32
	ParentID                  uint32
	ModuleName                string
	ClockID                   uint64
	BoardLabel                string
	PanelLabel                string
	PackageLabel              string
	Type                      uint32
	Direction                 uint32
	Frequency                 uint64
	FrequencySupported        []FrequencyRange
	FrequencyMin              uint64
	FrequencyMax              uint64
	Prio                      uint32
	State                     uint32
	Capabilities              uint32
	ParentDevice              []PinParentDevice
	ParentPin                 []PinParentPin
	PhaseAdjustMin            int32
	PhaseAdjustMax            int32
	PhaseAdjust               int32
	PhaseOffset               int64
	FractionalFrequencyOffset int
	EsyncFrequency            int64
	EsyncFrequencySupported   []FrequencyRange
	EsyncPulse                uint32
	ReferenceSync             []ReferenceSync
	PhaseAdjustGran           uint32
}

// FrequencyRange contains nested netlink attributes.
type FrequencyRange struct {
	FrequencyMin uint64 `json:"frequencyMin"`
	FrequencyMax uint64 `json:"frequencyMax"`
}

// ReferenceSync represents a reference-sync pin pair.
type ReferenceSync struct {
	ID    uint32 `json:"id"`
	State uint32 `json:"state"`
}

// PinParentDevice contains nested netlink attributes.
type PinParentDevice struct {
	ParentID    uint32
	Direction   uint32
	Prio        uint32
	State       uint32
	PhaseOffset int64
}

// PinParentPin contains nested netlink attributes.
type PinParentPin struct {
	ParentID uint32
	State    uint32
}

// PinPhaseAdjustRequest is used with PinPhaseAdjust method.
type PinPhaseAdjustRequest struct {
	ID          uint32
	PhaseAdjust int32
}

// PinPhaseAdjust wraps the "pin-set" operation:
// Set PhaseAdjust of a target pin
func (c *Conn) PinPhaseAdjust(req PinPhaseAdjustRequest) error {
	ae := netlink.NewAttributeEncoder()
	ae.Uint32(DpllPinID, req.ID)
	ae.Int32(DpllPinPhaseAdjust, req.PhaseAdjust)

	b, err := ae.Encode()
	if err != nil {
		return err
	}

	msg := genetlink.Message{
		Header: genetlink.Header{
			Command: DpllCmdPinSet,
			Version: c.f.Version,
		},
		Data: b,
	}

	// No replies.
	_, err = c.c.Send(msg, c.f.ID, netlink.Request)
	return err
}

type PinParentDeviceCtl struct {
	ID             uint32
	Frequency      *uint64
	PhaseAdjust    *int32
	EsyncFrequency *uint64
	PinParentCtl   []PinControl
}
type PinControl struct {
	PinParentID uint32
	Direction   *uint32
	Prio        *uint32
	State       *uint32
}

func EncodePinControl(req PinParentDeviceCtl) ([]byte, error) {
	ae := netlink.NewAttributeEncoder()
	ae.Uint32(DpllPinID, req.ID)
	if req.PhaseAdjust != nil {
		ae.Int32(DpllPinPhaseAdjust, *req.PhaseAdjust)
	}
	if req.EsyncFrequency != nil {
		ae.Uint64(DpllPinEsyncFrequency, *req.EsyncFrequency)
	}
	if req.Frequency != nil {
		ae.Uint64(DpllPinFrequency, *req.Frequency)
	}
	for _, pp := range req.PinParentCtl {
		ae.Nested(DpllPinParentDevice, func(ae *netlink.AttributeEncoder) error {
			ae.Uint32(DpllPinParentID, pp.PinParentID)
			if pp.State != nil {
				ae.Uint32(DpllPinState, *pp.State)
			}
			if pp.Prio != nil {
				ae.Uint32(DpllPinPrio, *pp.Prio)
			}
			if pp.Direction != nil {
				ae.Uint32(DpllPinDirection, *pp.Direction)
			}
			return nil
		})
	}
	b, err := ae.Encode()
	if err != nil {
		return []byte{}, err
	}
	return b, nil
}

// SendCommand sends DPLL commands that don't require waiting for a reply
func (c *Conn) SendCommand(command uint8, data []byte) error {
	msg := genetlink.Message{
		Header: genetlink.Header{
			Command: command,
			Version: c.f.Version,
		},
		Data: data,
	}
	// No replies.
	_, err := c.c.Send(msg, c.f.ID, netlink.Request)
	return err
}
