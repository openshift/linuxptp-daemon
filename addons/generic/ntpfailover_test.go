package generic

import (
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
)

func newTestPluginData(gnssFailover bool) *interface{} {
	pluginData := &ntpFailoverPluginData{
		gnssFailover:    gnssFailover,
		pcfsmState:      pcsmsStartupDefault,
		startupDelay:    90 * time.Second,
		ts2phcTolerance: 5 * time.Second,
		expiryTime:      time.Now().Add(90 * time.Second),
	}
	var iface interface{} = pluginData
	return &iface
}

func TestProcessLogNtpFailover_StartupGoesActive(t *testing.T) {
	data := newTestPluginData(true)
	pluginData := (*data).(*ntpFailoverPluginData)
	ch := make(chan event.Event, 4)
	pluginData.eventChannel = ch

	out := processLogNtpFailover(data, ts2phcPname, "ts2phc[123]: nmea delay")
	if out != "ts2phc[123]: nmea delay" {
		t.Fatalf("expected log passthrough, got %q", out)
	}
	if pluginData.pcfsmState != pcsmsActive {
		t.Fatalf("expected pcsmsActive, got %d", pluginData.pcfsmState)
	}
	select {
	case ev := <-ch:
		t.Fatalf("startup must not emit plugin events, got %+v", ev)
	default:
	}
}

func TestProcessLogNtpFailover_FailoverAndRecoverEmitPluginEvents(t *testing.T) {
	data := newTestPluginData(true)
	pluginData := (*data).(*ntpFailoverPluginData)
	ch := make(chan event.Event, 4)
	pluginData.eventChannel = ch
	pluginData.expiryTime = time.Now().Add(-time.Second)

	processLogNtpFailover(data, ts2phcPname, "ts2phc[123]: nmea delay")
	if pluginData.pcfsmState != pcsmsFailover {
		t.Fatalf("expected pcsmsFailover, got %d", pluginData.pcfsmState)
	}
	select {
	case ev := <-ch:
		pd, ok := ev.Data.(*event.PluginData)
		if !ok || pd.EventName != "gnss_failover" {
			t.Fatalf("expected gnss_failover, got %+v", ev)
		}
	default:
		t.Fatal("expected gnss_failover plugin event")
	}

	pluginData.expiryTime = time.Now().Add(time.Minute)
	processLogNtpFailover(data, ts2phcPname, "ts2phc[123]: nmea delay")
	if pluginData.pcfsmState != pcsmsActive {
		t.Fatalf("expected pcsmsActive after recover, got %d", pluginData.pcfsmState)
	}
	select {
	case ev := <-ch:
		pd, ok := ev.Data.(*event.PluginData)
		if !ok || pd.EventName != "gnss_recovered" {
			t.Fatalf("expected gnss_recovered, got %+v", ev)
		}
	default:
		t.Fatal("expected gnss_recovered plugin event")
	}
}

func TestProcessLogNtpFailover_TryLockExcludesConcurrentDrivers(t *testing.T) {
	data := newTestPluginData(true)
	pluginData := (*data).(*ntpFailoverPluginData)

	pluginData.pcfsmMutex.Lock()
	processLogNtpFailover(data, ts2phcPname, "line-while-locked")
	if pluginData.pcfsmState != pcsmsStartupDefault {
		pluginData.pcfsmMutex.Unlock()
		t.Fatalf("state should remain default while locked, got %d", pluginData.pcfsmState)
	}
	pluginData.pcfsmMutex.Unlock()

	processLogNtpFailover(data, ts2phcPname, "line-after-unlock")
	if pluginData.pcfsmState != pcsmsActive {
		t.Fatalf("expected pcsmsActive after unlock, got %d", pluginData.pcfsmState)
	}
}

func TestProcessLogNtpFailover_DisabledWhenGnssFailoverFalse(t *testing.T) {
	data := newTestPluginData(false)
	pluginData := (*data).(*ntpFailoverPluginData)
	ch := make(chan event.Event, 1)
	pluginData.eventChannel = ch

	processLogNtpFailover(data, ts2phcPname, "ts2phc[123]: nmea delay")
	if pluginData.pcfsmState != pcsmsStartupDefault {
		t.Fatalf("state should remain default, got %d", pluginData.pcfsmState)
	}
	select {
	case ev := <-ch:
		t.Fatalf("must not emit events when gnssFailover is false, got %+v", ev)
	default:
	}
}
