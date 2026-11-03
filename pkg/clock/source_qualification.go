package clock

import (
	"sync/atomic"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
)

var sourceGeneration atomic.Uint64

// PTPPortSource identifies a physical upstream PTP port. Other input kinds can
// implement utils.QualificationSource without depending on PTP port names.
type PTPPortSource struct {
	Port string
}

// Equal reports whether other identifies the same PTP port.
func (s PTPPortSource) Equal(other utils.QualificationSource) bool {
	port, ok := other.(PTPPortSource)
	return ok && s.Port == port.Port
}

// String returns the PTP port's interface name.
func (s PTPPortSource) String() string { return s.Port }

type ptpSourceQualification struct {
	state                   *utils.QualificationState
	configuredUpstreamPorts []string
	lastSelectedPort        string
	available               bool
	generation              uint64
}

func (q *ptpSourceQualification) source() utils.QualificationSource {
	if q.lastSelectedPort == "" {
		return nil
	}
	return PTPPortSource{Port: q.lastSelectedPort}
}
