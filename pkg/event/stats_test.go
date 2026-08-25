package event_test

import (
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/stretchr/testify/assert"
	"testing"
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
