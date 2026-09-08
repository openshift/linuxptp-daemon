package generic

import (
	"encoding/json"
	"regexp"
	"sync"
	"time"

	"github.com/golang/glog"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

type ntpFailoverPluginData struct {
	gnssFailover    bool
	eventChannel    chan<- event.Event
	pcfsmState      int
	pcfsmMutex      sync.Mutex
	ts2phcTolerance time.Duration
	startupDelay    time.Duration
	expiryTime      time.Time
}

type ntpFailoverOpts struct {
	StartupDelay    string `json:"startupDelay"`
	Ts2phcTolerance string `json:"ts2phcTolerance"` //nolint:stylecheck
	GnssFailover    bool   `json:"gnssFailover"`
}

const ( // phc2sys/chronyd Finite State Machine States
	pcsmsStartupDefault int = iota // Just started, waiting for first log line
	pcsmsActive                    // phc2sys setting time, ts2phc healthy
	pcsmsOutOfSpec                 // ts2phc stale, waiting for confirmation
	pcsmsFailover                  // chronyd setting time
)

const (
	ts2phcPname = "ts2phc"
)

var (
	ts2phcOffsetRegex = regexp.MustCompile("offset .*s3 freq")
)

func onPTPConfigChangeNtpFailover(data *interface{}, nodeProfile *ptpv1.PtpProfile) error {
	var _ntpFailoverOpts ntpFailoverOpts
	_ntpFailoverOpts.StartupDelay = "90s"
	_ntpFailoverOpts.Ts2phcTolerance = "5s"
	_ntpFailoverOpts.GnssFailover = false
	var err error
	if data != nil {
		_data := *data
		var pluginData = _data.(*ntpFailoverPluginData)
		for name, opts := range (*nodeProfile).Plugins {
			if name == "ntpfailover" {
				optsByteArray, _ := json.Marshal(opts)
				err = json.Unmarshal(optsByteArray, &_ntpFailoverOpts)
				if err != nil {
					glog.Error("ntpfailover failed to unmarshal opts: " + err.Error())
				}
			}
		}

		pluginData.gnssFailover = _ntpFailoverOpts.GnssFailover

		pluginData.startupDelay, err = time.ParseDuration(_ntpFailoverOpts.StartupDelay)
		if err != nil {
			glog.Infof("Failed parsing startupDelay %s: %d.  Defaulting to 90 seconds.", _ntpFailoverOpts.StartupDelay, err)
			pluginData.startupDelay, _ = time.ParseDuration("90s")
		}
		pluginData.expiryTime = time.Now().Add(pluginData.startupDelay)

		pluginData.ts2phcTolerance, err = time.ParseDuration(_ntpFailoverOpts.Ts2phcTolerance)
		if err != nil {
			glog.Infof("Failed parsing ts2phcTolerance %s: %d.  Defaulting to 5 seconds.", _ntpFailoverOpts.Ts2phcTolerance, err)
			pluginData.ts2phcTolerance, _ = time.ParseDuration("5s")
		}
	}
	return nil
}

func (pd *ntpFailoverPluginData) emitEvent(eventName string) {
	if pd.eventChannel != nil {
		pd.eventChannel <- event.PluginEvent("ntpfailover", eventName)
	}
}

func processLogNtpFailover(data *interface{}, pname string, log string) string {
	ret := log
	if data != nil {
		_data := *data

		var pluginData = _data.(*ntpFailoverPluginData)
		if pluginData.gnssFailover {
			currentTime := time.Now()

			if pname == ts2phcPname && ts2phcOffsetRegex.MatchString(log) {
				pluginData.expiryTime = currentTime.Add(pluginData.ts2phcTolerance)
			}

			// Non-blocking: another ProcessLog may already be driving the FSM.
			if !pluginData.pcfsmMutex.TryLock() {
				return ret
			}
			defer pluginData.pcfsmMutex.Unlock()
		done:
			for {
				switch pluginData.pcfsmState {
				case pcsmsStartupDefault:
					pluginData.pcfsmState = pcsmsActive
					continue
				case pcsmsActive:
					if pname == ts2phcPname {
						if currentTime.After(pluginData.expiryTime) {
							pluginData.pcfsmState = pcsmsOutOfSpec
							continue
						}
					}
					break done
				case pcsmsOutOfSpec:
					if pname == ts2phcPname {
						if currentTime.After(pluginData.expiryTime) {
							pluginData.pcfsmState = pcsmsFailover
							glog.Infof("ntpfailover: GNSS failover triggered")
							pluginData.emitEvent("gnss_failover")
							continue
						}
					}
					break done
				case pcsmsFailover:
					if pname == ts2phcPname {
						if currentTime.Before(pluginData.expiryTime) {
							pluginData.pcfsmState = pcsmsStartupDefault
							glog.Infof("ntpfailover: GNSS recovered")
							pluginData.emitEvent("gnss_recovered")
							continue
						}
					}
					break done
				}
			}
		}
	}

	return ret
}

func setEventChannelNtpFailover(data *interface{}, ch chan<- event.Event) {
	if data != nil {
		_data := *data
		var pluginData = _data.(*ntpFailoverPluginData)
		pluginData.eventChannel = ch
	}
}

// NtpFailover initializes NtpFailover plugin
func NtpFailover(name string) (*plugin.Plugin, *interface{}) {
	if name != "ntpfailover" {
		glog.Errorf("Plugin must be initialized as 'ntpfailover'")
		return nil, nil
	}
	glog.Infof("registering ntpfailover plugin")
	_plugin := plugin.Plugin{Name: "ntpfailover",
		OnPTPConfigChange: onPTPConfigChangeNtpFailover,
		SetEventChannel:   setEventChannelNtpFailover,
		ProcessLog:        processLogNtpFailover,
	}
	pluginData := ntpFailoverPluginData{pcfsmState: pcsmsStartupDefault,
		pcfsmMutex: sync.Mutex{}}
	var iface interface{} = &pluginData
	return &_plugin, &iface
}
