package leap

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang/glog"
	"github.com/openshift/linuxptp-daemon/pkg/pmc"
	"github.com/openshift/linuxptp-daemon/pkg/ublox"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	defaultLeapFileName    = "leap-seconds.list"
	defaultLeapFilePath    = "/usr/share/zoneinfo"
	gpsToTaiDiff           = 19
	curreLsValidMask       = 0x1
	timeToLsEventValidMask = 0x2
	leapSourceGps          = 2
	leapConfigMapName      = "leap-configmap"
	MaintenancePeriod      = time.Minute * 1
	pmcWindowStartHours    = 12
	pmcWindowEndSeconds    = 60
)

type LeapManager struct {
	// Ublox GNSS leap time indications channel
	UbloxLsInd chan ublox.TimeLs
	// Close channel
	Close chan bool
	// ts2phc path of leap-seconds.list file
	LeapFilePath string
	// client
	client    kubernetes.Interface
	namespace string
	// Leap file structure
	leapFile LeapFile
	// Retry configmap update if failed
	retryUpdate bool
	// Default leap file path and name
	leapFilePath string
	leapFileName string
	// UTC offset and its validity time
	utcOffset       int
	utcOffsetTime   time.Time
	ptp4lConfigPath string
	pmcLeapSent     bool
}

type LeapEvent struct {
	LeapTime string `json:"leapTime"`
	LeapSec  int    `json:"leapSec"`
	Comment  string `json:"comment"`
}
type LeapFile struct {
	ExpirationTime string      `json:"expirationTime"`
	UpdateTime     string      `json:"updateTime"`
	LeapEvents     []LeapEvent `json:"leapEvents"`
	Hash           string      `json:"hash"`
}

var lock = &sync.Mutex{}

var LeapMgr *LeapManager

func New(kubeclient kubernetes.Interface, namespace string) (*LeapManager, error) {
	if LeapMgr == nil {
		lock.Lock()
		defer lock.Unlock()
		if LeapMgr == nil {
			lm := &LeapManager{
				UbloxLsInd:   make(chan ublox.TimeLs, 2),
				Close:        make(chan bool),
				client:       kubeclient,
				namespace:    namespace,
				leapFile:     LeapFile{},
				leapFilePath: defaultLeapFilePath,
				leapFileName: defaultLeapFileName,
			}
			err := lm.populateLeapData()
			if err != nil {
				return nil, err
			}
			LeapMgr = lm
		}
	}
	return LeapMgr, nil
}

func GetUtcOffset() int {
	if LeapMgr != nil {
		if time.Now().UTC().After(LeapMgr.utcOffsetTime) {
			return LeapMgr.utcOffset
		} else if len(LeapMgr.leapFile.LeapEvents) > 1 {
			return LeapMgr.leapFile.LeapEvents[len(LeapMgr.leapFile.LeapEvents)-2].LeapSec
		}
	}
	glog.Fatal("failed to get UTC offset")
	return 0
}

func (l *LeapManager) setUtcOffset() error {
	startTime := time.Date(1900, time.January, 1, 0, 0, 0, 0, time.UTC)
	lastLeap := l.leapFile.LeapEvents[len(l.leapFile.LeapEvents)-1]
	lastLeapTime, err := strconv.Atoi(lastLeap.LeapTime)
	if err != nil {
		return err
	}
	l.utcOffsetTime = startTime.Add(time.Second * time.Duration(lastLeapTime))
	l.utcOffset = lastLeap.LeapSec
	return nil
}

func parseLeapFile(b []byte) (*LeapFile, error) {
	var l = LeapFile{}
	lines := strings.Split(string(b), "\n")
	for i := 0; i < len(lines); i++ {
		fields := strings.Fields(lines[i])
		if strings.HasPrefix(lines[i], "#$") {
			l.UpdateTime = fields[1]
		} else if strings.HasPrefix(lines[i], "#@") {
			l.ExpirationTime = fields[1]
		} else if strings.HasPrefix(lines[i], "#h") {
			l.Hash = strings.Join(fields[1:], " ")
		} else if !strings.HasPrefix(lines[i], "#") {
			if len(fields) < 2 {
				// empty line
				continue
			}
			sec, err := strconv.ParseInt(fields[1], 10, 8)
			if err != nil {
				return nil, fmt.Errorf("failed to parse Leap seconds %s value: %s", fields[1], err)
			}
			if _, err := strconv.ParseInt(fields[0], 10, 64); err != nil {
				return nil, fmt.Errorf("failed to parse Leap event time %s: %s", fields[0], err)
			}
			ev := LeapEvent{
				LeapTime: fields[0],
				LeapSec:  int(sec),
				Comment:  strings.Join(fields[2:], " "),
			}
			l.LeapEvents = append(l.LeapEvents, ev)
		}
	}
	return &l, nil
}

func (l *LeapManager) SetPtp4lConfigPath(path string) {
	glog.Info("set Leap manager ptp4l config file name to ", path)
	l.ptp4lConfigPath = path
}

func (l *LeapManager) renderLeapData() (*bytes.Buffer, error) {
	templateStr := `# Do not edit
# This file is generated automatically by linuxptp-daemon
#$	{{ .UpdateTime }}
#@	{{ .ExpirationTime }}
{{ range .LeapEvents }}{{ .LeapTime }}     {{ .LeapSec }}    {{ .Comment }}
{{ end }}
#h	{{ .Hash }}`

	templ, err := template.New("leap").Parse(templateStr)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	bufWriter := bufio.NewWriter(&buf)

	err = templ.Execute(bufWriter, l.leapFile)
	if err != nil {
		return nil, err
	}
	bufWriter.Flush()
	return &buf, nil
}

func (l *LeapManager) populateLeapData() error {
	cm, err := l.client.CoreV1().ConfigMaps(l.namespace).Get(context.TODO(), leapConfigMapName, metav1.GetOptions{})
	nodeName := os.Getenv("NODE_NAME")
	if err != nil {
		return err
	}
	lf, found := cm.Data[nodeName]
	if !found {
		glog.Info("Populate Leap data from file")
		b, err := os.ReadFile(filepath.Join(l.leapFilePath, l.leapFileName))
		if err != nil {
			return err
		}
		leapData, err := parseLeapFile(b)
		if err != nil {
			return err
		}
		l.leapFile = *leapData
		// Set expiration time to 2036
		exp := time.Date(2036, time.January, 1, 0, 0, 0, 0, time.UTC)
		start := time.Date(1900, time.January, 1, 0, 0, 0, 0, time.UTC)
		expSec := int(exp.Sub(start).Seconds())
		l.leapFile.ExpirationTime = fmt.Sprint(expSec)
		l.rehashLeapData()
		data, err := l.renderLeapData()
		if err != nil {
			return err
		}
		if len(cm.Data) == 0 {
			cm.Data = map[string]string{}
		}
		cm.Data[nodeName] = data.String()
		_, err = l.client.CoreV1().ConfigMaps(l.namespace).Update(context.TODO(), cm, metav1.UpdateOptions{})
		if err != nil {
			l.retryUpdate = true
			return err
		}
	} else {
		glog.Info("Populate Leap data from configmap")
		leapData, err := parseLeapFile([]byte(lf))
		if err != nil {
			return err
		}
		l.leapFile = *leapData
	}
	glog.Info("Leap file expiration is set to ", l.leapFile.ExpirationTime)
	l.setUtcOffset()
	return nil
}

func (l *LeapManager) Run() {
	glog.Info("starting Leap file manager")
	ticker := time.NewTicker(MaintenancePeriod)
	for {
		select {
		case v := <-l.UbloxLsInd:
			l.handleLeapIndication(&v)
		case <-l.Close:
			LeapMgr = nil
			return
		case <-ticker.C:
			if l.retryUpdate {
				l.updateLeapConfigmap()
			}
			if l.IsLeapInWindow(time.Now().UTC(), -pmcWindowStartHours*time.Hour, -pmcWindowEndSeconds*time.Second) {
				if !l.pmcLeapSent {
					g, err := pmc.RunPMCExpGetGMSettings(l.ptp4lConfigPath)
					if err != nil {
						glog.Error("error in Leap:", err)
						continue
					}
					leapDiff := l.leapFile.LeapEvents[len(l.leapFile.LeapEvents)-1].LeapSec - int(g.TimePropertiesDS.CurrentUtcOffset)
					if leapDiff > 0 {
						g.TimePropertiesDS.Leap59 = false
						g.TimePropertiesDS.Leap61 = true
					} else if leapDiff < 0 {
						g.TimePropertiesDS.Leap59 = true
						g.TimePropertiesDS.Leap61 = false
					} else {
						// No actual change in leap seconds, don't send anything
						l.pmcLeapSent = true
						continue
					}
					glog.Info("Sending PMC command in Leap window")
					glog.Infof("Leap time properties: %++v", g.TimePropertiesDS)
					err = pmc.RunPMCExpSetGMSettings(l.ptp4lConfigPath, g)
					if err != nil {
						glog.Error("failed to send PMC for Leap: ", err)
						continue
					}
					l.pmcLeapSent = true
				}
			} else {
				l.pmcLeapSent = false
			}
		}
	}
}

// updateLeapFile updates a new leap event to the list of leap events, if provided
func (l *LeapManager) updateLeapFile(leapTime time.Time,
	leapSec int, currentTime time.Time) {

	startTime := time.Date(1900, time.January, 1, 0, 0, 0, 0, time.UTC)
	if leapSec != 0 {
		leapTimeTai := int(leapTime.Sub(startTime).Seconds())

		ev := LeapEvent{
			LeapTime: fmt.Sprint(leapTimeTai),
			LeapSec:  leapSec,
			Comment: fmt.Sprintf("# %v %v %v",
				leapTime.Day(), leapTime.Month().String()[:3], leapTime.Year()),
		}
		l.leapFile.LeapEvents = append(l.leapFile.LeapEvents, ev)
	}
	l.leapFile.UpdateTime = fmt.Sprint(int(currentTime.Sub(startTime).Seconds()))
	l.rehashLeapData()
	l.setUtcOffset()
}

func (l *LeapManager) rehashLeapData() {
	data := fmt.Sprint(l.leapFile.UpdateTime) + fmt.Sprint(l.leapFile.ExpirationTime)

	for _, ev := range l.leapFile.LeapEvents {
		data += fmt.Sprint(ev.LeapTime) + fmt.Sprint(ev.LeapSec)
	}
	// checksum
	hash := fmt.Sprintf("%x", sha1.Sum([]byte(data)))
	var groupedHash string
	// group checksum by 8 characters
	for i := 0; i < 5; i++ {
		if groupedHash != "" {
			groupedHash += " "
		}
		groupedHash += hash[i*8 : (i+1)*8]
	}
	l.leapFile.Hash = groupedHash
}

func (l *LeapManager) updateLeapConfigmap() {
	data, err := l.renderLeapData()
	if err != nil {
		glog.Error("Leap: ", err)
		return
	}
	cm, err := l.client.CoreV1().ConfigMaps(l.namespace).Get(context.TODO(), leapConfigMapName, metav1.GetOptions{})

	if err != nil {
		l.retryUpdate = true
		glog.Info("failed to get leap configmap (will retry): ", err)
		return
	}
	nodeName := os.Getenv("NODE_NAME")
	if len(cm.Data) == 0 {
		cm.Data = map[string]string{}
	}
	cm.Data[nodeName] = data.String()
	_, err = l.client.CoreV1().ConfigMaps(l.namespace).Update(context.TODO(), cm, metav1.UpdateOptions{})
	if err != nil {
		l.retryUpdate = true
		glog.Info("failed to update leap configmap (will retry): ", err)
		return
	}
	l.retryUpdate = false
}

func (l *LeapManager) handleLeapIndication(data *ublox.TimeLs) {
	r, err := l.processLeapIndication(data)
	if err != nil {
		glog.Error(err)
	}
	if r != nil {
		l.updateLeapFile(r.leapTime, r.leapSec, r.updateTime)
		l.updateLeapConfigmap()
	}
}

type leapIndResult struct {
	leapTime   time.Time
	leapSec    int
	updateTime time.Time
}

// processLeapIndication handles NAV-TIMELS indication
// and updates the leapseconds.list file
func (l *LeapManager) processLeapIndication(data *ublox.TimeLs) (*leapIndResult, error) {

	glog.Infof("Leap indication: %+v", data)
	if data.SrcOfCurrLs != leapSourceGps {
		glog.Info("Discarding Leap event not originating from GPS")
		return nil, nil
	}
	result := &leapIndResult{}
	startOfTimes := time.Date(1900, time.January, 1, 0, 0, 0, 0, time.UTC)
	// Last leap seconds on file
	leapSecOnFile := l.leapFile.LeapEvents[len(l.leapFile.LeapEvents)-1].LeapSec
	sec, err := strconv.Atoi(l.leapFile.LeapEvents[len(l.leapFile.LeapEvents)-1].LeapTime)
	if err != nil {
		return nil, fmt.Errorf("failed to convert Leap time to seconds: %v", err)
	}
	fileLsDate := startOfTimes.Add(time.Duration(sec) * time.Second)
	currentTime := time.Now().UTC()
	fileLsPassed := fileLsDate.Before(currentTime)

	validCurrLs := (data.Valid & curreLsValidMask) > 0
	validTimeToLsEvent := (data.Valid & timeToLsEventValidMask) > 0
	if validTimeToLsEvent && validCurrLs {
		leapSec := int(data.CurrLs) + gpsToTaiDiff + int(data.LsChange)
		if leapSec != leapSecOnFile && fileLsPassed {
			// File update is needed
			glog.Infof("Leap Seconds on file outdated: %d on file, %d + %d + %d in GNSS data",
				leapSecOnFile, int(data.CurrLs), gpsToTaiDiff, int(data.LsChange))
			gpsStartTime := time.Date(1980, time.January, 6, 0, 0, 0, 0, time.UTC)
			deltaHours, err := time.ParseDuration(fmt.Sprintf("%dh",
				data.DateOfLsGpsWn*7*24+uint(data.DateOfLsGpsDn)*24))
			if err != nil {
				return nil, fmt.Errorf("failed to parse time duration: Leap: %v", err)
			}
			if data.LsChange == 0 && data.TimeToLsEvent >= 0 {
				// shift leap date out of pmc window, so no pmc commands will be sent
				result.leapTime = currentTime.Add(-pmcWindowEndSeconds * time.Second)
			} else {
				result.leapTime = gpsStartTime.Add(deltaHours)
			}
			result.updateTime = currentTime
			result.leapSec = leapSec
			return result, nil
		}
	}
	return nil, nil
}

// IsLeapInWindow() returns whether a leap event is occuring within the specified time window from now
func (l *LeapManager) IsLeapInWindow(now time.Time, startOffset, endOffset time.Duration) bool {
	startTime := time.Date(1900, time.January, 1, 0, 0, 0, 0, time.UTC)
	lastLeap := l.leapFile.LeapEvents[len(l.leapFile.LeapEvents)-1]
	lastLeapTime, err := strconv.Atoi(lastLeap.LeapTime)
	if err != nil {
		return false
	}
	leapTime := startTime.Add(time.Second * time.Duration(lastLeapTime))
	leapWindowStart := leapTime.Add(startOffset)
	leapWindowEnd := leapTime.Add(endOffset)
	if now.After(leapWindowStart) && now.Before(leapWindowEnd) {
		glog.Info("Leap in window: ", startOffset, " ", endOffset)
		return true
	}
	return false
}
