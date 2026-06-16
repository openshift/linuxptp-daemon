package main

import (
	"flag"
	"log"
	"net"
	"strconv"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
)

func main() {
	socket := flag.String("socket", "/var/run/ptp/events.sock", "Path to cloud-event-proxy Unix socket")
	msgType := flag.String("type", "", "IPC message type (e.g. clock_class, ptp_state, gnss_state)")
	profile := flag.String("profile", "ptp4l.0.config", "Profile name")
	iface := flag.String("iface", "", "Interface name")
	state := flag.String("state", "", "State value (for state-type messages)")
	clockClass := flag.String("clock-class", "", "Clock class value (for clock_class messages)")
	flag.Parse()

	if *msgType == "" {
		log.Fatal("--type is required")
	}

	msg := ipc.Message{
		Version: ipc.Version,
		Type:    *msgType,
		Profile: *profile,
		IFace:   *iface,
	}

	switch *msgType {
	case ipc.TypePTPState, ipc.TypeOSClockState:
		if *state == "" {
			log.Fatal("--state is required for state-type messages")
		}
		msg.Values = ipc.StateValue{State: *state}
	case ipc.TypeGNSSState:
		if *state == "" {
			log.Fatal("--state is required for state-type messages")
		}
		msg.Values = ipc.GNSSStateValue{State: *state}
	case ipc.TypeSyncEState:
		if *state == "" {
			log.Fatal("--state is required for state-type messages")
		}
		msg.Values = ipc.SyncEStateValue{State: *state}
	case ipc.TypeSyncState:
		if *state == "" {
			log.Fatal("--state is required for state-type messages")
		}
		msg.Values = ipc.SyncStateValue{State: *state}

	case ipc.TypeClockClass:
		if *clockClass == "" {
			log.Fatal("--clock-class is required for clock_class messages")
		}
		cc, err := strconv.ParseUint(*clockClass, 10, 8)
		if err != nil {
			log.Fatalf("invalid clock-class value: %v", err)
		}
		msg.Values = ipc.ClockClassValue{ClockClass: uint8(cc)}

	case ipc.TypeCacheClear:
		// no values needed

	default:
		log.Fatalf("unsupported message type: %s", *msgType)
	}

	conn, err := net.Dial("unix", *socket)
	if err != nil {
		log.Fatalf("failed to connect to socket %s: %v", *socket, err)
	}
	defer conn.Close()

	if encodeErr := ipc.Encode(conn, []ipc.Message{msg}); encodeErr != nil {
		log.Fatalf("failed to send message: %v", encodeErr)
	}
}
