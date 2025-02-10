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

// GetMcastGroupId finds the requested multicast group in the family and returns its ID
func (c *Conn) GetMcastGroupId(mcGroup string) (id uint32, found bool) {
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

// DoDeviceIdGet wraps the "device-id-get" operation:
// Get id of dpll device that matches given attributes
func (c *Conn) DoDeviceIdGet(req DoDeviceIdGetRequest) (*DoDeviceIdGetReply, error) {
	ae := netlink.NewAttributeEncoder()
	// TODO: field "req.ModuleName", type "string"
	if req.ClockId != 0 {
		ae.Uint64(DPLL_A_CLOCK_ID, req.ClockId)
	}
	if req.Type != 0 {
		ae.Uint8(DPLL_A_TYPE, req.Type)
	}

	b, err := ae.Encode()
	if err != nil {
		return nil, err
	}

	msg := genetlink.Message{
		Header: genetlink.Header{
			Command: DPLL_CMD_DEVICE_ID_GET,
			Version: c.f.Version,
		},
		Data: b,
	}

	msgs, err := c.c.Execute(msg, c.f.ID, netlink.Request)
	if err != nil {
		return nil, err
	}

	replies := make([]*DoDeviceIdGetReply, 0, len(msgs))
	for _, m := range msgs {
		ad, err := netlink.NewAttributeDecoder(m.Data)
		if err != nil {
			return nil, err
		}

		var reply DoDeviceIdGetReply
		for ad.Next() {
			switch ad.Type() {
			case DPLL_A_ID:
				reply.Id = ad.Uint32()
			}
		}

		if err := ad.Err(); err != nil {
			return nil, err
		}

		replies = append(replies, &reply)
	}

	if len(replies) != 1 {
		return nil, errors.New("dpll: expected exactly one DoDeviceIdGetReply")
	}

	return replies[0], nil
}

// DoDeviceIdGetRequest is used with the DoDeviceIdGet method.
type DoDeviceIdGetRequest struct {
	// TODO: field "ModuleName", type "string"
	ClockId uint64
	Type    uint8
}

// DoDeviceIdGetReply is used with the DoDeviceIdGet method.
type DoDeviceIdGetReply struct {
	Id uint32
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
			case DPLL_A_ID:
				reply.Id = ad.Uint32()
			case DPLL_A_MODULE_NAME:
				reply.ModuleName = ad.String()
			case DPLL_A_MODE:
				reply.Mode = ad.Uint32()
			case DPLL_A_MODE_SUPPORTED:
				reply.ModeSupported = append(reply.ModeSupported, ad.Uint32())
			case DPLL_A_LOCK_STATUS:
				reply.LockStatus = ad.Uint32()
			case DPLL_A_PAD:
			case DPLL_A_TEMP:
				reply.Temp = ad.Int32()
			case DPLL_A_CLOCK_ID:
				reply.ClockId = ad.Uint64()
			case DPLL_A_TYPE:
				reply.Type = ad.Uint32()
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
	if req.Id != 0 {
		ae.Uint32(DPLL_A_ID, req.Id)
	}
	// TODO: field "req.ModuleName", type "string"

	b, err := ae.Encode()
	if err != nil {
		return nil, err
	}

	msg := genetlink.Message{
		Header: genetlink.Header{
			Command: DPLL_CMD_DEVICE_GET,
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
			Command: DPLL_CMD_DEVICE_GET,
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
	Id         uint32
	ModuleName string
}

// DoDeviceGetReply is used with the DoDeviceGet method.
type DoDeviceGetReply struct {
	Id            uint32
	ModuleName    string
	Mode          uint32
	ModeSupported []uint32
	LockStatus    uint32
	Temp          int32
	ClockId       uint64
	Type          uint32
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
			case DPLL_A_PIN_CLOCK_ID:
				reply.ClockId = ad.Uint64()
			case DPLL_A_PIN_ID:
				reply.Id = ad.Uint32()
			case DPLL_A_PIN_BOARD_LABEL:
				reply.BoardLabel = ad.String()
			case DPLL_A_PIN_PANEL_LABEL:
				reply.PanelLabel = ad.String()
			case DPLL_A_PIN_PACKAGE_LABEL:
				reply.PackageLabel = ad.String()
			case DPLL_A_PIN_TYPE:
				reply.Type = ad.Uint32()
			case DPLL_A_PIN_FREQUENCY:
				reply.Frequency = ad.Uint64()
			case DPLL_A_PIN_FREQUENCY_SUPPORTED:
				ad.Nested(func(ad *netlink.AttributeDecoder) error {
					var temp FrequencyRange
					for ad.Next() {
						switch ad.Type() {
						case DPLL_A_PIN_FREQUENCY_MIN:
							temp.FrequencyMin = ad.Uint64()
						case DPLL_A_PIN_FREQUENCY_MAX:
							temp.FrequencyMax = ad.Uint64()
						}
					}
					reply.FrequencySupported = append(reply.FrequencySupported, temp)
					return nil
				})
			case DPLL_A_PIN_CAPABILITIES:
				reply.Capabilities = ad.Uint32()
			case DPLL_A_PIN_PARENT_DEVICE:
				ad.Nested(func(ad *netlink.AttributeDecoder) error {
					temp := PinParentDevice{
						// Initialize phase offset to a max value, so later we can detect it has been updated
						PhaseOffset: math.MaxInt64,
					}
					for ad.Next() {
						switch ad.Type() {
						case DPLL_A_PIN_PARENT_ID:
							temp.ParentId = ad.Uint32()
						case DPLL_A_PIN_DIRECTION:
							temp.Direction = ad.Uint32()
						case DPLL_A_PIN_PRIO:
							temp.Prio = ad.Uint32()
						case DPLL_A_PIN_STATE:
							temp.State = ad.Uint32()
						case DPLL_A_PIN_PHASE_OFFSET:
							temp.PhaseOffset = ad.Int64()
						}

					}
					reply.ParentDevice = append(reply.ParentDevice, temp)
					return nil
				})
			case DPLL_A_PIN_PARENT_PIN:
				ad.Nested(func(ad *netlink.AttributeDecoder) error {
					var temp PinParentPin
					for ad.Next() {

						switch ad.Type() {
						case DPLL_A_PIN_PARENT_ID:
							temp.ParentId = ad.Uint32()
						case DPLL_A_PIN_STATE:
							temp.State = ad.Uint32()
						}

					}
					reply.ParentPin = append(reply.ParentPin, temp)
					return nil
				})
			case DPLL_A_PIN_PHASE_ADJUST_MIN:
				reply.PhaseAdjustMin = ad.Int32()
			case DPLL_A_PIN_PHASE_ADJUST_MAX:
				reply.PhaseAdjustMax = ad.Int32()
			case DPLL_A_PIN_PHASE_ADJUST:
				reply.PhaseAdjust = ad.Int32()
			case DPLL_A_PIN_FRACTIONAL_FREQUENCY_OFFSET:
				reply.FractionalFrequencyOffset = int(ad.Int32())
			case DPLL_A_PIN_MODULE_NAME:
				reply.ModuleName = ad.String()
			default:
				log.Println(ad.Bytes())
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
	ae.Uint32(DPLL_A_PIN_ID, req.Id)

	b, err := ae.Encode()
	if err != nil {
		return nil, err
	}

	msg := genetlink.Message{
		Header: genetlink.Header{
			Command: DPLL_CMD_PIN_GET,
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
			Command: DPLL_CMD_PIN_GET,
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
	Id uint32
}

// PinInfo is used with the DoPinGet method.
type PinInfo struct {
	Id                        uint32
	ClockId                   uint64
	BoardLabel                string
	PanelLabel                string
	PackageLabel              string
	Type                      uint32
	Frequency                 uint64
	FrequencySupported        []FrequencyRange
	Capabilities              uint32
	ParentDevice              []PinParentDevice
	ParentPin                 []PinParentPin
	PhaseAdjustMin            int32
	PhaseAdjustMax            int32
	PhaseAdjust               int32
	FractionalFrequencyOffset int
	ModuleName                string
}

// FrequencyRange contains nested netlink attributes.
type FrequencyRange struct {
	FrequencyMin uint64 `json:"frequencyMin"`
	FrequencyMax uint64 `json:"frequencyMax"`
}

// PinParentDevice contains nested netlink attributes.
type PinParentDevice struct {
	ParentId    uint32
	Direction   uint32
	Prio        uint32
	State       uint32
	PhaseOffset int64
}

// PinParentPin contains nested netlink attributes.
type PinParentPin struct {
	ParentId uint32
	State    uint32
}

// PinPhaseAdjustRequest is used with PinPhaseAdjust method.
type PinPhaseAdjustRequest struct {
	Id          uint32
	PhaseAdjust int32
}

// PinPhaseAdjust wraps the "pin-set" operation:
// Set PhaseAdjust of a target pin
func (c *Conn) PinPhaseAdjust(req PinPhaseAdjustRequest) error {
	ae := netlink.NewAttributeEncoder()
	ae.Uint32(DPLL_A_PIN_ID, req.Id)
	ae.Int32(DPLL_A_PIN_PHASE_ADJUST, req.PhaseAdjust)

	b, err := ae.Encode()
	if err != nil {
		return err
	}

	msg := genetlink.Message{
		Header: genetlink.Header{
			Command: DPLL_CMD_PIN_SET,
			Version: c.f.Version,
		},
		Data: b,
	}

	// No replies.
	_, err = c.c.Send(msg, c.f.ID, netlink.Request)
	return err
}

type PinParentDeviceCtl struct {
	Id           uint32
	PhaseAdjust  *int32
	PinParentCtl []PinControl
}
type PinControl struct {
	PinParentId uint32
	Direction   *uint32
	Prio        *uint32
	State       *uint32
}

func EncodePinControl(req PinParentDeviceCtl) ([]byte, error) {
	ae := netlink.NewAttributeEncoder()
	ae.Uint32(DPLL_A_PIN_ID, req.Id)
	if req.PhaseAdjust != nil {
		ae.Int32(DPLL_A_PIN_PHASE_ADJUST, *req.PhaseAdjust)
	}
	for _, pp := range req.PinParentCtl {
		ae.Nested(DPLL_A_PIN_PARENT_DEVICE, func(ae *netlink.AttributeEncoder) error {
			ae.Uint32(DPLL_A_PIN_PARENT_ID, pp.PinParentId)
			if pp.State != nil {
				ae.Uint32(DPLL_A_PIN_STATE, *pp.State)
			}
			if pp.Prio != nil {
				ae.Uint32(DPLL_A_PIN_PRIO, *pp.Prio)
			}
			if pp.Direction != nil {
				ae.Uint32(DPLL_A_PIN_DIRECTION, *pp.Direction)
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
