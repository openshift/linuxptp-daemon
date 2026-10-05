package event_test

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/stretchr/testify/assert"
)

const (
	statsTestEns7f0Iface = "ens7f0"
	statsTestEns1f0Iface = "ens1f0"
)

type testDataSet struct {
	data        map[string][]*event.Data
	wantedState event.PTPState
	desc        string
}

func Test_updateStats(t *testing.T) {
	tests := []testDataSet{{
		data: map[string][]*event.Data{
			"a.0.config": {
				{
					ProcessName: "ts2phc",
					Details: []*event.DataDetails{
						{
							IFace:     "en01",
							State:     event.PTP_FREERUN,
							ClockType: "GM",
							Metrics:   nil,
						},
						{
							IFace:     "en201",
							State:     event.PTP_LOCKED,
							ClockType: "GM",
							Metrics:   nil,
						},
					},
					State: event.PTP_UNKNOWN,
				}}},
		wantedState: event.PTP_FREERUN,
		desc:        "0. GM is FREERUN and PPS is LOCKED ",
	}, {
		data: map[string][]*event.Data{
			"a.0.config": {
				{
					Details: []*event.DataDetails{
						{
							IFace:     "en01",
							State:     event.PTP_LOCKED,
							ClockType: "GM",
							Metrics:   nil,
						},
						{
							IFace:     "en201",
							State:     event.PTP_FREERUN,
							ClockType: "GM",
							Metrics:   nil,
						},
					},
					State: event.PTP_UNKNOWN,
				}}},
		wantedState: event.PTP_FREERUN,
		desc:        "1. GNSS LOCKED PPS is FREERUN",
	}, {
		data: map[string][]*event.Data{
			"a.0.config": {
				{
					ProcessName: "ts2phc",
					Details: []*event.DataDetails{
						{
							IFace:     "en01",
							State:     event.PTP_HOLDOVER,
							ClockType: "GM",
							Metrics:   nil,
						},
						{
							IFace:     "en201",
							State:     event.PTP_LOCKED,
							ClockType: "GM",
							Metrics:   nil,
						},
					},
					State: event.PTP_UNKNOWN,
				}}},
		wantedState: event.PTP_HOLDOVER,
		desc:        "2. GNSS is in HOLDOVER PPS is in LOCKED",
	}, {
		data: map[string][]*event.Data{
			"a.0.config": {
				{
					ProcessName: "ts2phc",
					Details: []*event.DataDetails{
						{
							IFace:   "en01",
							State:   event.PTP_HOLDOVER,
							Metrics: nil,
						},
						{
							IFace:     "en201",
							State:     event.PTP_FREERUN,
							ClockType: "GM",
							Metrics:   nil,
						},
					},
					State: event.PTP_UNKNOWN,
				}}},
		wantedState: event.PTP_FREERUN,
		desc:        "3. GNSS is in HOLDOVER, PPS is in FREERUN - FREERUN takes priority (worst state)",
	}, {
		data: map[string][]*event.Data{
			"a.0.config": {
				{
					ProcessName: "ts2phc",
					Details: []*event.DataDetails{
						{
							IFace:   "en01",
							State:   event.PTP_LOCKED,
							Metrics: nil,
						},
						{
							IFace:     "en201",
							State:     event.PTP_LOCKED,
							ClockType: "GM",
							Metrics:   nil,
						},
					},
					State: event.PTP_UNKNOWN,
				}}},
		wantedState: event.PTP_LOCKED,
		desc:        "4. Both are in locked state",
	}}

	for _, test := range tests {
		for _, d := range test.data {
			for _, dd := range d {
				dd.UpdateState()
				assert.Equal(t, test.wantedState, dd.State, test.desc)
			}

		}

	}

}

func Test_updateState_LeadingFollowerMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		desc          string
		leadingState  event.PTPState
		followerState event.PTPState
		wantedState   event.PTPState
	}{
		{
			desc:          "both locked",
			leadingState:  event.PTP_LOCKED,
			followerState: event.PTP_LOCKED,
			wantedState:   event.PTP_LOCKED,
		},
		{
			desc:          "follower freerun, leader locked - follower degrades to S0",
			leadingState:  event.PTP_LOCKED,
			followerState: event.PTP_FREERUN,
			wantedState:   event.PTP_FREERUN,
		},
		{
			desc:          "follower freerun, leader holdover - FREERUN wins over HOLDOVER",
			leadingState:  event.PTP_HOLDOVER,
			followerState: event.PTP_FREERUN,
			wantedState:   event.PTP_FREERUN,
		},
		{
			desc:          "leader holdover, follower locked - HOLDOVER propagates",
			leadingState:  event.PTP_HOLDOVER,
			followerState: event.PTP_LOCKED,
			wantedState:   event.PTP_HOLDOVER,
		},
		{
			desc:          "both freerun",
			leadingState:  event.PTP_FREERUN,
			followerState: event.PTP_FREERUN,
			wantedState:   event.PTP_FREERUN,
		},
		{
			desc:          "leader freerun, follower locked - FREERUN wins",
			leadingState:  event.PTP_FREERUN,
			followerState: event.PTP_LOCKED,
			wantedState:   event.PTP_FREERUN,
		},
		{
			desc:          "both holdover",
			leadingState:  event.PTP_HOLDOVER,
			followerState: event.PTP_HOLDOVER,
			wantedState:   event.PTP_HOLDOVER,
		},
		{
			desc:          "leader locked, follower holdover",
			leadingState:  event.PTP_LOCKED,
			followerState: event.PTP_HOLDOVER,
			wantedState:   event.PTP_HOLDOVER,
		},
		{
			desc:          "leader freerun, follower holdover - FREERUN wins",
			leadingState:  event.PTP_FREERUN,
			followerState: event.PTP_HOLDOVER,
			wantedState:   event.PTP_FREERUN,
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			t.Parallel()
			d := &event.Data{
				ProcessName: "dpll",
				Details: []*event.DataDetails{
					{
						IFace:   "leading-nic",
						State:   tt.leadingState,
						Metrics: map[event.ValueType]event.DataMetric{},
					},
					{
						IFace:   "follower-nic",
						State:   tt.followerState,
						Metrics: map[event.ValueType]event.DataMetric{},
					},
				},
				State: event.PTP_UNKNOWN,
			}
			d.UpdateState()
			assert.Equal(t, tt.wantedState, d.State, tt.desc)
		})
	}
}

func TestDataDetails_String_GNSSLike(t *testing.T) {
	dd := &event.DataDetails{
		IFace:      statsTestEns7f0Iface,
		State:      event.PTP_LOCKED,
		Offset:     12,
		HasOffset:  true,
		SourceLost: false,
	}
	assert.Equal(t, "iface=ens7f0 state=s2 offset=12", dd.String())

	dd.SourceLost = true
	dd.State = event.PTP_FREERUN
	dd.Offset = 0
	assert.Equal(t, "iface=ens7f0 state=s0 offset=0 sourceLost=true", dd.String())
}

func TestData_String_DPLLMultiPort(t *testing.T) {
	d := &event.Data{
		ProcessName: event.DPLL,
		Details: []*event.DataDetails{
			{IFace: statsTestEns7f0Iface, State: event.PTP_LOCKED, Offset: 5, HasOffset: true},
			{IFace: "ens8f0", State: event.PTP_FREERUN, Offset: 99, HasOffset: true},
		},
	}
	assert.Equal(t, "dpll {iface=ens7f0 state=s2 offset=5} {iface=ens8f0 state=s0 offset=99}", d.String())
}

func TestDataDetails_String_OmitsUnsetLockAndOffset(t *testing.T) {
	dd := &event.DataDetails{IFace: "eth0", State: event.PTP_UNKNOWN, Offset: 0}
	assert.Equal(t, "iface=eth0", dd.String())
	dd.State = event.PTP_NOTSET
	assert.Equal(t, "iface=eth0", dd.String())
	dd.State = ""
	assert.Equal(t, "iface=eth0", dd.String())
}

func TestData_Summary_LastFieldsAndCount(t *testing.T) {
	d := &event.Data{
		ProcessName: event.TS2PHC,
		Details: []*event.DataDetails{
			{IFace: "ens8f0", State: event.PTP_LOCKED, Offset: 5, HasOffset: true, Time: 2},
			{IFace: statsTestEns1f0Iface, State: event.PTP_LOCKED, Offset: 3, HasOffset: true, Time: 3},
			{IFace: "ens4f0", State: event.PTP_LOCKED, Offset: 0, HasOffset: true, Time: 4},
		},
	}
	assert.Equal(t, "ts2phc n=3 iface=ens4f0 state=s2 offset=0", d.Summary())

	gnss := &event.Data{
		ProcessName: event.GNSS,
		Details: []*event.DataDetails{
			{IFace: "ens4f0", State: event.PTP_LOCKED, Offset: 8, HasOffset: true, Time: 10},
			{IFace: "ens4f0", State: event.PTP_FREERUN, Offset: 99, HasOffset: true, SourceLost: true, Time: 11},
		},
	}
	assert.Equal(t, "gnss n=2 iface=ens4f0 state=s0 offset=99 sourceLost=true", gnss.Summary())

	gpspipe := &event.Data{
		ProcessName: event.GPSPIPE,
	}
	assert.Equal(t, "gpspipe n=0", gpspipe.Summary())
}
