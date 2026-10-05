package clock

import (
	"testing"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testHACfg      = "ptp4l.ha.config"
	testHAProfile1 = "bc-profile1"
	testHAProfile2 = "bc-profile2"
)

// newTestHAClock builds an HA clock with two member BC clocks (ptp4l.0.config and
// ptp4l.1.config) sharing one OS clock and one IPC recorder. The OS clock starts
// LOCKED so overall-state assertions isolate the best-of-members behavior unless a
// test changes it.
func newTestHAClock() (*HAClock, *ipcRecorder, *OsClock) {
	rio := &ipcRecorder{}
	os := &OsClock{State: event.PTP_LOCKED}
	newMember := func(cfg string) *BCClock {
		return &BCClock{
			BaseClock: BaseClock{
				cfgName:          cfg,
				overallSyncState: event.PTP_NOTSET,
				osClock:          os,
				threshold:        testThreshold,
			},
			syncState: event.PTP_NOTSET,
		}
	}
	m1 := newMember(testPTP4lCfg)      // ptp4l.0.config
	m2 := newMember(testControlledCfg) // ptp4l.1.config
	ha := &HAClock{
		BaseClock: BaseClock{
			cfgName:          testHACfg,
			overallSyncState: event.PTP_NOTSET,
			osClock:          os,
		},
		members:        map[string]*BCClock{testPTP4lCfg: m1, testControlledCfg: m2},
		memberProfiles: map[string]string{testPTP4lCfg: testHAProfile1, testControlledCfg: testHAProfile2},
		syncState:      event.PTP_NOTSET,
	}
	// Wire IPC through SetIPC so members get the HA interceptor, not the raw sender.
	ha.SetIPC(rio.send)
	return ha, rio, os
}

// haOffsetEvent builds a ptp4l offset event tagged with the member config name so
// the HA clock can route it.
func haOffsetEvent(cfg, iface string, offset int64) event.Event {
	return event.Event{CfgName: cfg, IFace: iface, Data: &event.OffsetData{Offset: offset}}
}

// haSourceLostEvent builds a ptp4l source-lost event for a member.
func haSourceLostEvent(cfg, iface string) event.Event {
	return event.Event{CfgName: cfg, IFace: iface, Data: &event.OffsetData{SourceLost: true}}
}

// haSelectEvent builds a phc2sys source-selection event for the given interface.
func haSelectEvent(iface string) event.Event {
	return event.Event{CfgName: testHACfg, IFace: iface, Data: &event.SelectedSourceData{IFace: iface}}
}

func TestBestOfState(t *testing.T) {
	tests := []struct {
		a, b     event.PTPState
		expected event.PTPState
	}{
		{event.PTP_LOCKED, event.PTP_LOCKED, event.PTP_LOCKED},
		{event.PTP_LOCKED, event.PTP_FREERUN, event.PTP_LOCKED},
		{event.PTP_FREERUN, event.PTP_LOCKED, event.PTP_LOCKED},
		{event.PTP_HOLDOVER, event.PTP_LOCKED, event.PTP_LOCKED},
		{event.PTP_HOLDOVER, event.PTP_FREERUN, event.PTP_HOLDOVER},
		{event.PTP_FREERUN, event.PTP_HOLDOVER, event.PTP_HOLDOVER},
		{event.PTP_FREERUN, event.PTP_FREERUN, event.PTP_FREERUN},
		{event.PTP_HOLDOVER, event.PTP_HOLDOVER, event.PTP_HOLDOVER},
		{event.PTP_NOTSET, event.PTP_LOCKED, event.PTP_LOCKED},
		{event.PTP_NOTSET, event.PTP_FREERUN, event.PTP_FREERUN},
		{event.PTP_NOTSET, event.PTP_NOTSET, event.PTP_NOTSET},
	}
	for _, tt := range tests {
		t.Run(string(tt.a)+"_"+string(tt.b), func(t *testing.T) {
			assert.Equal(t, tt.expected, bestOfState(tt.a, tt.b))
		})
	}
}

func TestHAClock_Interface(t *testing.T) {
	ha, _, _ := newTestHAClock()
	assert.Equal(t, event.HA, ha.ClockType())
	assert.Equal(t, testHACfg, ha.ConfigName())
}

func TestHAClock_BestOfMembers(t *testing.T) {
	t.Run("one member LOCKED, other FREERUN -> LOCKED", func(t *testing.T) {
		ha, _, _ := newTestHAClock()
		ha.AddEvent(haOffsetEvent(testControlledCfg, testEns7f0, 5000))  // m2 FREERUN
		cs := ha.AddEvent(haOffsetEvent(testPTP4lCfg, testTBCIface, 10)) // m1 LOCKED
		assert.Equal(t, event.PTP_LOCKED, cs.State)
		assert.Equal(t, event.PTP_LOCKED, ha.GetState())
	})

	t.Run("both members FREERUN -> FREERUN", func(t *testing.T) {
		ha, _, _ := newTestHAClock()
		ha.AddEvent(haOffsetEvent(testPTP4lCfg, testTBCIface, 5000))
		cs := ha.AddEvent(haOffsetEvent(testControlledCfg, testEns7f0, 5000))
		assert.Equal(t, event.PTP_FREERUN, cs.State)
	})

	t.Run("both members LOCKED -> LOCKED", func(t *testing.T) {
		ha, _, _ := newTestHAClock()
		ha.AddEvent(haOffsetEvent(testPTP4lCfg, testTBCIface, 10))
		cs := ha.AddEvent(haOffsetEvent(testControlledCfg, testEns7f0, 10))
		assert.Equal(t, event.PTP_LOCKED, cs.State)
	})

	t.Run("one member HOLDOVER, other FREERUN -> HOLDOVER", func(t *testing.T) {
		ha, _, _ := newTestHAClock()
		// m1 LOCKED then source lost -> HOLDOVER; m2 FREERUN.
		ha.AddEvent(haOffsetEvent(testPTP4lCfg, testTBCIface, 10))
		ha.AddEvent(haOffsetEvent(testControlledCfg, testEns7f0, 5000))
		cs := ha.AddEvent(haSourceLostEvent(testPTP4lCfg, testTBCIface))
		require.Equal(t, event.PTP_HOLDOVER, ha.members[testPTP4lCfg].GetState())
		assert.Equal(t, event.PTP_HOLDOVER, cs.State)
		ha.members[testPTP4lCfg].cancelHoldoverTimer()
	})
}

func TestHAClock_Failover(t *testing.T) {
	// Primary (m1) is the good source, secondary (m2) is freerun. When the
	// primary's source is lost the composite coasts in HOLDOVER; when the
	// secondary then locks, phc2sys has a healthy source again so the composite
	// returns to LOCKED. This is the redundancy guarantee: a dead primary must not
	// hold the node in FREERUN while a healthy secondary exists.
	ha, _, _ := newTestHAClock()

	ha.AddEvent(haOffsetEvent(testPTP4lCfg, testTBCIface, 10))      // m1 LOCKED
	ha.AddEvent(haOffsetEvent(testControlledCfg, testEns7f0, 5000)) // m2 FREERUN
	require.Equal(t, event.PTP_LOCKED, ha.GetState())

	// Primary source lost -> composite holds (secondary still freerun).
	ha.AddEvent(haSourceLostEvent(testPTP4lCfg, testTBCIface))
	assert.Equal(t, event.PTP_HOLDOVER, ha.GetState())

	// Secondary locks -> failover, composite LOCKED again.
	cs := ha.AddEvent(haOffsetEvent(testControlledCfg, testEns7f0, 10))
	assert.Equal(t, event.PTP_LOCKED, cs.State)

	ha.members[testPTP4lCfg].cancelHoldoverTimer()
}

func TestHAClock_OverallWithOSClock(t *testing.T) {
	t.Run("best-of members LOCKED but OS clock FREERUN -> overall FREERUN", func(t *testing.T) {
		ha, rio, os := newTestHAClock()
		ha.AddEvent(haOffsetEvent(testPTP4lCfg, testTBCIface, 10)) // composite LOCKED
		require.Equal(t, event.PTP_LOCKED, ha.GetState())

		os.State = event.PTP_FREERUN
		rio.messages = nil
		ha.SystemClockUpdate()

		assert.Equal(t, event.PTP_FREERUN, ha.overallSyncState)
		msg := findHAStateMessage(rio.messages, testHACfg)
		require.NotNil(t, msg, "HA clock should emit its own sync_state")
		assert.Equal(t, ipc.SyncStateValue{State: ipc.StateFreerun}, msg.Values)
	})

	t.Run("best-of members LOCKED and OS clock LOCKED -> overall LOCKED", func(t *testing.T) {
		ha, _, _ := newTestHAClock() // os starts LOCKED
		ha.AddEvent(haOffsetEvent(testPTP4lCfg, testTBCIface, 10))
		assert.Equal(t, event.PTP_LOCKED, ha.overallSyncState)
	})
}

func TestHAClock_InterceptsMemberSyncState(t *testing.T) {
	// A member drives its own state and would normally publish an overall
	// sync_state under its member config name. The HA clock must intercept that so
	// only the composite sync_state (under the HA profile) reaches CEP.
	ha, rio, _ := newTestHAClock()

	ha.AddEvent(haOffsetEvent(testPTP4lCfg, testTBCIface, 10)) // member m1 -> LOCKED

	// No sync_state may be published under a member profile.
	assert.Nil(t, findHAStateMessage(rio.messages, testPTP4lCfg),
		"member sync_state must be intercepted, not published")
	assert.Nil(t, findHAStateMessage(rio.messages, testControlledCfg),
		"member sync_state must be intercepted, not published")

	// The composite sync_state is published under the HA profile.
	require.NotNil(t, findHAStateMessage(rio.messages, testHACfg),
		"HA clock must publish the composite sync_state")

	// The member's per-port ptp_state still passes through.
	var memberPTPState *ipc.Message
	for i := range rio.messages {
		if rio.messages[i].Type == ipc.TypePTPState && rio.messages[i].Profile == testPTP4lCfg {
			memberPTPState = &rio.messages[i]
		}
	}
	require.NotNil(t, memberPTPState, "member per-port ptp_state should pass through")
	assert.Equal(t, testTBCIface, memberPTPState.IFace)
}

func TestHAClock_ProfileStatus(t *testing.T) {
	t.Run("no member is active before a phc2sys selection", func(t *testing.T) {
		ha, _, _ := newTestHAClock()
		cs := ha.AddEvent(haOffsetEvent(testPTP4lCfg, testTBCIface, 10)) // LOCKED, but not selected

		require.NotNil(t, cs.HAProfileStatus)
		assert.False(t, cs.HAProfileStatus[testHAProfile1], "active comes from phc2sys, not best-of")
		assert.False(t, cs.HAProfileStatus[testHAProfile2])
	})

	t.Run("selected member is active, others inactive, keyed by profile name", func(t *testing.T) {
		ha, _, _ := newTestHAClock()
		ha.AddEvent(haOffsetEvent(testPTP4lCfg, testTBCIface, 10))      // m1 (profile1) learns its iface
		ha.AddEvent(haOffsetEvent(testControlledCfg, testEns7f0, 5000)) // m2 (profile2) learns its iface
		cs := ha.AddEvent(haSelectEvent(testTBCIface))                  // phc2sys selects m1

		assert.True(t, cs.HAProfileStatus[testHAProfile1], "selected member is active")
		assert.False(t, cs.HAProfileStatus[testHAProfile2], "non-selected member is inactive")
	})
}

func TestHAClock_SelectedSource(t *testing.T) {
	t.Run("selection sets the active member", func(t *testing.T) {
		ha, _, _ := newTestHAClock()
		// Both members LOCKED; no member is active until phc2sys selects one.
		ha.AddEvent(haOffsetEvent(testPTP4lCfg, testTBCIface, 10))
		ha.AddEvent(haOffsetEvent(testControlledCfg, testEns7f0, 10))
		require.False(t, ha.currentSyncState().HAProfileStatus[testHAProfile1])
		require.False(t, ha.currentSyncState().HAProfileStatus[testHAProfile2])

		// phc2sys selects the second member's interface.
		cs := ha.AddEvent(haSelectEvent(testEns7f0))
		assert.False(t, cs.HAProfileStatus[testHAProfile1])
		assert.True(t, cs.HAProfileStatus[testHAProfile2], "selected member must be active")
		// Composite state is unchanged by a selection (still best-of).
		assert.Equal(t, event.PTP_LOCKED, cs.State)
	})

	t.Run("selection of unknown interface keeps the previous active", func(t *testing.T) {
		ha, _, _ := newTestHAClock()
		ha.AddEvent(haOffsetEvent(testPTP4lCfg, testTBCIface, 10))
		ha.AddEvent(haOffsetEvent(testControlledCfg, testEns7f0, 10))
		ha.AddEvent(haSelectEvent(testEns7f0)) // active = profile2

		cs := ha.AddEvent(haSelectEvent("does-not-exist"))
		assert.True(t, cs.HAProfileStatus[testHAProfile2], "unknown selection must not change active")
		assert.False(t, cs.HAProfileStatus[testHAProfile1])
	})
}

func TestHAClock_AddEventLeadingIface(t *testing.T) {
	// clockmgr emits per-interface clock_state from the returned SyncState.LeadingIFace.
	// Member events must report that member's own follower interface (so each BC
	// follower gets its own clock_state), while a source-selection event must report
	// none (it only updates ha_profile_status, it is not a clock_state sample).
	t.Run("member event reports that member's follower interface", func(t *testing.T) {
		ha, _, _ := newTestHAClock()
		cs1 := ha.AddEvent(haOffsetEvent(testPTP4lCfg, testTBCIface, 10))
		assert.Equal(t, testTBCIface, cs1.LeadingIFace)
		assert.Equal(t, event.PTP_LOCKED, cs1.State)

		cs2 := ha.AddEvent(haOffsetEvent(testControlledCfg, testEns7f0, 10))
		assert.Equal(t, testEns7f0, cs2.LeadingIFace, "second member reports its own follower, not the first")
	})

	t.Run("selection event reports no leading interface", func(t *testing.T) {
		ha, _, _ := newTestHAClock()
		ha.AddEvent(haOffsetEvent(testPTP4lCfg, testTBCIface, 10))
		cs := ha.AddEvent(haSelectEvent(testTBCIface))
		assert.Empty(t, cs.LeadingIFace, "a selection event must not drive a clock_state series")
		assert.True(t, cs.HAProfileStatus[testHAProfile1], "but it must still update ha_profile_status")
	})
}

func TestHAClock_PerMemberClockClass(t *testing.T) {
	// clockmgr keys openshift_ptp_clock_class per member config off the ClockClass in
	// the SyncState returned by AddEvent. Each member event must therefore report
	// that member's own class — not the active member's composite class — so a
	// freerun member reports 248 even while the active member is locked at 6.
	parentDS := func(cfg string, class uint8) event.Event {
		return event.Event{
			Source:  event.PMC,
			CfgName: cfg,
			Data:    &event.ParentDSData{ParentDataSet: protocol.ParentDataSet{GrandmasterClockClass: class}},
		}
	}

	ha, _, _ := newTestHAClock()
	// Member 1 locked to GM class 6 and selected as the active source.
	ha.AddEvent(parentDS(testPTP4lCfg, 6))
	ha.AddEvent(haOffsetEvent(testPTP4lCfg, testTBCIface, 10))
	ha.AddEvent(haSelectEvent(testTBCIface))
	require.Equal(t, fbprotocol.ClockClass(6), ha.ClockClass(), "composite reports the active member's class")

	// Member 2 goes freerun -> its own class becomes 248.
	cs := ha.AddEvent(parentDS(testControlledCfg, 248))
	assert.Equal(t, fbprotocol.ClockClass(248), cs.ClockClass,
		"member event must report that member's class, not the active member's")
	assert.Equal(t, fbprotocol.ClockClass(6), ha.ClockClass(),
		"composite class is unchanged by a non-active member's update")
}

func TestHAClock_UnknownConfigDropped(t *testing.T) {
	ha, _, _ := newTestHAClock()
	ha.AddEvent(haOffsetEvent(testPTP4lCfg, testTBCIface, 10)) // composite LOCKED
	require.Equal(t, event.PTP_LOCKED, ha.GetState())

	// An event for a config that is not a member must not change state.
	cs := ha.AddEvent(haOffsetEvent("ptp4l.9.config", "ensX", 99999))
	assert.Equal(t, event.PTP_LOCKED, cs.State)
	assert.Equal(t, event.PTP_LOCKED, ha.GetState())
}

func TestHAClock_Reset(t *testing.T) {
	ha, _, _ := newTestHAClock()
	ha.AddEvent(haOffsetEvent(testPTP4lCfg, testTBCIface, 10))
	ha.AddEvent(haOffsetEvent(testControlledCfg, testEns7f0, 10))
	require.Equal(t, event.PTP_LOCKED, ha.GetState())

	ha.Reset()
	assert.Equal(t, event.PTP_FREERUN, ha.syncState)
	assert.Equal(t, event.PTP_FREERUN, ha.overallSyncState)
	for cfg, m := range ha.members {
		assert.Equal(t, event.PTP_FREERUN, m.GetState(), "member %s should reset to FREERUN", cfg)
	}
}

// findHAStateMessage returns the first sync_state IPC message published under the
// given profile, or nil.
func findHAStateMessage(msgs []ipc.Message, profile string) *ipc.Message {
	for i := range msgs {
		if msgs[i].Type == ipc.TypeSyncState && msgs[i].Profile == profile {
			return &msgs[i]
		}
	}
	return nil
}
