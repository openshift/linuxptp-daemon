package generic

import (
	"encoding/json"
	"regexp"
	"sync"
	"time"

	"github.com/golang/glog"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

type ntpFailoverPluginData struct {
	gnssFailover    bool
	cmdSetEnabled   map[string]func(bool)
	pcfsmState      int
	pcfsmMutex      sync.Mutex
	pcfsmLocked     bool
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
	pcsmsStartupDefault int = iota // Just started, both are unknown
	pcsmsStartupPhc2sys            // phc2sys setting time
	pcsmsStartupChronyd            // phc2sys setting time
	pcsmsStartupBoth               // phc2sys setting time
	pcsmsActive                    // phc2sys setting time
	pcsmsOutOfSpec                 // switch to chronyd setting time
	pcsmsFailover                  // chronyd setting time
)

const (
	ts2phcPname  = "ts2phc"
	chronydPname = "chronyd"
	phc2sysPname = "phc2sys"
)

var (
	ts2phcOffsetRegex  = regexp.MustCompile("offset .*s[23] freq")
	chronydOnlineRegex = regexp.MustCompile("chronyd .* starting")
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
		if pluginData.cmdSetEnabled == nil {
			pluginData.cmdSetEnabled = make(map[string]func(bool))
		}
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

func registerProcessNtpFailover(data *interface{}, pname string, cmdSetEnabled func(bool)) {
	if data != nil {
		_data := *data

		var pluginData = _data.(*ntpFailoverPluginData)
		if pluginData.gnssFailover {
			if pluginData.cmdSetEnabled == nil {
				pluginData.cmdSetEnabled = make(map[string]func(bool))
			}
			pluginData.cmdSetEnabled[pname] = cmdSetEnabled
		}
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

			pluginData.pcfsmMutex.Lock()
			ownLock := !pluginData.pcfsmLocked //If locked, then skip, otherwise take lock
			pluginData.pcfsmMutex.Unlock()
			if ownLock {
			done:
				for {
					switch pluginData.pcfsmState {
					case pcsmsStartupDefault:
						_, foundChronyd := pluginData.cmdSetEnabled[chronydPname]
						_, foundPhc2Sys := pluginData.cmdSetEnabled[phc2sysPname]
						if foundChronyd && foundPhc2Sys {
							pluginData.pcfsmState = pcsmsStartupBoth
						} else if foundChronyd {
							pluginData.pcfsmState = pcsmsStartupChronyd
						} else if foundPhc2Sys {
							pluginData.pcfsmState = pcsmsStartupPhc2sys
						} else {
							break done
						}
					case pcsmsStartupPhc2sys:
						_, foundChronyd := pluginData.cmdSetEnabled[chronydPname]
						if foundChronyd {
							pluginData.pcfsmState = pcsmsStartupBoth
						} else {
							break done
						}
					case pcsmsStartupChronyd:
						_, foundPhc2Sys := pluginData.cmdSetEnabled[phc2sysPname]
						if foundPhc2Sys {
							pluginData.pcfsmState = pcsmsStartupBoth
						} else {
							break done
						}
					case pcsmsStartupBoth:
						chronydSetEnabled, ok := pluginData.cmdSetEnabled[chronydPname]
						if ok {
							chronydSetEnabled(false)
						}
						phc2sysSetEnabled, ok := pluginData.cmdSetEnabled[phc2sysPname]
						if ok {
							phc2sysSetEnabled(true)
						}
						pluginData.pcfsmState = pcsmsActive
						continue
					case pcsmsActive:
						if pname == ts2phcPname {
							if currentTime.After(pluginData.expiryTime) {
								pluginData.pcfsmState = pcsmsOutOfSpec
								continue
							}
						}
						if pname == chronydPname && chronydOnlineRegex.MatchString(log) {
							chronydSetEnabled, ok := pluginData.cmdSetEnabled[chronydPname]
							if ok {
								chronydSetEnabled(false)
							}
						}
						break done
					case pcsmsOutOfSpec:
						if pname == ts2phcPname {
							if currentTime.After(pluginData.expiryTime) {
								pluginData.pcfsmState = pcsmsFailover
								chronydSetEnabled, ok := pluginData.cmdSetEnabled[chronydPname]
								if ok {
									chronydSetEnabled(true)
								}
								phc2sysSetEnabled, ok := pluginData.cmdSetEnabled[phc2sysPname]
								if ok {
									phc2sysSetEnabled(false)
								}
								continue
							}
						}
						break done
					case pcsmsFailover:
						if pname == ts2phcPname {
							if currentTime.Before(pluginData.expiryTime) {
								pluginData.pcfsmState = pcsmsStartupDefault
								continue
							}
						}
						break done
					}
				}
				pluginData.pcfsmMutex.Lock()
				pluginData.pcfsmLocked = false //If took lock, then return it
				pluginData.pcfsmMutex.Unlock()
			}
		}
	}

	return ret
}

// NtpFailover initializes NtpFailover plugin
func NtpFailover(name string) (*plugin.Plugin, *interface{}) {
	if name != "ntpfailover" {
		glog.Errorf("Plugin must be initialized as 'ntpfailover'")
		return nil, nil
	}
	glog.Infof("registering ntpfailover plugin")
	_plugin := plugin.Plugin{Name: "ntpfailover",
		OnPTPConfigChange:      onPTPConfigChangeNtpFailover,
		RegisterEnableCallback: registerProcessNtpFailover,
		ProcessLog:             processLogNtpFailover,
	}
	pluginData := ntpFailoverPluginData{pcfsmState: pcsmsStartupDefault,
		pcfsmMutex: sync.Mutex{}}
	pluginData.cmdSetEnabled = make(map[string]func(bool))
	var iface interface{} = &pluginData
	return &_plugin, &iface
}
