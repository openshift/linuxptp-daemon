package leap

import (
	"bytes"
	"fmt"
	"os"
	"testing"
	"time"

	leaphash "github.com/facebook/time/leaphash"
	"github.com/openshift/linuxptp-daemon/pkg/ublox"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fake "k8s.io/client-go/kubernetes/fake"
)

func readTestData(t *testing.T) *LeapFile {
	leapFile := "testdata/leap-seconds.list"
	b, err := os.ReadFile(leapFile)
	assert.Equal(t, nil, err)
	leapData, err := parseLeapFile(b)
	assert.Equal(t, nil, err)
	return leapData
}

func Test_updateLeapFile(t *testing.T) {
	leapTime := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	updateTime := time.Date(2024, time.May, 8, 0, 0, 0, 0, time.UTC)
	leapSec := 38
	leapData := readTestData(t)
	lm := &LeapManager{
		UbloxLsInd: make(chan ublox.TimeLs),
		Close:      make(chan bool),
		leapFile:   *leapData,
	}
	lm.updateLeapFile(leapTime, leapSec, updateTime)
	buf, err := lm.renderLeapData()
	assert.Equal(t, nil, err)
	hash := leaphash.Compute(buf.String())
	assert.Equal(t, hash, lm.leapFile.Hash)
}

func Test_RenderLeapFile(t *testing.T) {
	leapData := readTestData(t)
	lm := &LeapManager{
		UbloxLsInd: make(chan ublox.TimeLs),
		Close:      make(chan bool),
		leapFile:   *leapData,
	}
	l, err := lm.renderLeapData()

	assert.Equal(t, nil, err)
	desired, err := os.ReadFile("testdata/leap-seconds.list.rendered")
	assert.Equal(t, nil, err)
	assert.True(t, bytes.Equal(l.Bytes(), desired))
}

// This testcase checks no update
func Test_processLeapIndication_FutureLeapZero(t *testing.T) {
	leapData := readTestData(t)
	lm := &LeapManager{
		UbloxLsInd: make(chan ublox.TimeLs),
		Close:      make(chan bool),
		leapFile:   *leapData,
	}

	ind := ublox.TimeLs{
		SrcOfCurrLs:   2,
		CurrLs:        18,
		SrcOfLsChange: 2,
		LsChange:      0,
		TimeToLsEvent: 74329702,
		DateOfLsGpsWn: 2441,
		DateOfLsGpsDn: 7,
		Valid:         3,
	}
	res, err := lm.processLeapIndication(&ind)
	assert.NoError(t, err)
	assert.Nil(t, res)
}

func Test_processLeapIndication_FutureLeapNotZero(t *testing.T) {
	leapData := readTestData(t)
	lm := &LeapManager{
		UbloxLsInd: make(chan ublox.TimeLs),
		Close:      make(chan bool),
		leapFile:   *leapData,
	}

	ind := ublox.TimeLs{
		SrcOfCurrLs:   2,
		CurrLs:        18,
		SrcOfLsChange: 2,
		LsChange:      1,
		TimeToLsEvent: 74329702,
		DateOfLsGpsWn: 2441,
		DateOfLsGpsDn: 7,
		Valid:         3,
	}
	res, err := lm.processLeapIndication(&ind)
	assert.NoError(t, err)
	assert.NotNil(t, res)
	assert.WithinDuration(t, time.Now().UTC(), res.updateTime, 1*time.Second)
	deltaHours, err := time.ParseDuration(fmt.Sprintf("%dh",
		ind.DateOfLsGpsWn*7*24+uint(ind.DateOfLsGpsDn)*24))
	assert.NoError(t, err)
	assert.WithinDuration(t, res.leapTime, time.Date(1980, time.January, 6, 0, 0, 0, 0, time.UTC).Add(deltaHours), 0*time.Second)
	assert.Equal(t, int(ind.CurrLs+ind.LsChange+19), res.leapSec)
}

func Test_processLeapIndication_MissedLeapZero(t *testing.T) {
	leapData := readTestData(t)
	lm := &LeapManager{
		UbloxLsInd: make(chan ublox.TimeLs),
		Close:      make(chan bool),
		leapFile:   *leapData,
	}

	ind := ublox.TimeLs{
		SrcOfCurrLs:   2,
		CurrLs:        19,
		SrcOfLsChange: 2,
		LsChange:      0,
		TimeToLsEvent: 74329702,
		DateOfLsGpsWn: 2441,
		DateOfLsGpsDn: 7,
		Valid:         3,
	}
	res, err := lm.processLeapIndication(&ind)
	assert.NoError(t, err)
	assert.NotNil(t, res)
	assert.WithinDuration(t, time.Now().UTC(), res.updateTime, 1*time.Second)
	assert.WithinDuration(t, res.leapTime, time.Now().UTC(), 1*time.Second)
	assert.Equal(t, int(ind.CurrLs+ind.LsChange+19), res.leapSec)
}

func Test_processLeapIndication_MissedLeapNotZero(t *testing.T) {
	leapData := readTestData(t)
	lm := &LeapManager{
		UbloxLsInd: make(chan ublox.TimeLs),
		Close:      make(chan bool),
		leapFile:   *leapData,
	}

	ind := ublox.TimeLs{
		SrcOfCurrLs:   2,
		CurrLs:        19,
		SrcOfLsChange: 2,
		LsChange:      0,
		TimeToLsEvent: -109267494,
		DateOfLsGpsWn: 2138, // (2021, 1, 1)
		DateOfLsGpsDn: 5,    // (2021, 1, 1)
		Valid:         3,
	}
	res, err := lm.processLeapIndication(&ind)
	assert.NoError(t, err)
	assert.NotNil(t, res)
	assert.WithinDuration(t, time.Now().UTC(), res.updateTime, 1*time.Second)
	assert.WithinDuration(t, res.leapTime, time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC), 1*time.Second)
	assert.Equal(t, int(ind.CurrLs+ind.LsChange+19), res.leapSec)
	fmt.Println(res.updateTime)
	fmt.Println(res.leapSec)
	fmt.Println(res.leapTime)
}

func Test_populateLeapDataCmGood(t *testing.T) {
	cm := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "openshift-ptp", Name: "leap-configmap"},
		Data: map[string]string{
			"test-node-name": `# Do not edit
# This file is generated automatically by linuxptp-daemon
#$	3927775672
#@	4291747200
3692217600     37    # 1 Jan 2017`,
		},
	}
	os.Setenv("NODE_NAME", "test-node-name")
	client := fake.NewSimpleClientset(cm)
	lm := &LeapManager{
		UbloxLsInd:  make(chan ublox.TimeLs),
		Close:       make(chan bool),
		client:      client,
		namespace:   "openshift-ptp",
		retryUpdate: false,
	}
	err := lm.populateLeapData()
	assert.NoError(t, err)
	assert.Equal(t, "4291747200", lm.leapFile.ExpirationTime)
	assert.Equal(t, "3927775672", lm.leapFile.UpdateTime)
	assert.Equal(t, "3692217600", lm.leapFile.LeapEvents[0].LeapTime)
	assert.Equal(t, 1, len(lm.leapFile.LeapEvents))
	assert.Equal(t, 37, lm.leapFile.LeapEvents[0].LeapSec)
}

func Test_populateLeapDataCmBadSec(t *testing.T) {
	cm := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "openshift-ptp", Name: "leap-configmap"},
		Data: map[string]string{
			"test-node-name": `# Do not edit
# This file is generated automatically by linuxptp-daemon
#$	3927775672
#@	4291747200
3692217600     3a    # 1 Jan 2017`,
		},
	}
	os.Setenv("NODE_NAME", "test-node-name")
	client := fake.NewSimpleClientset(cm)
	lm := &LeapManager{
		UbloxLsInd:  make(chan ublox.TimeLs),
		Close:       make(chan bool),
		client:      client,
		namespace:   "openshift-ptp",
		retryUpdate: false,
	}
	err := lm.populateLeapData()
	assert.Error(t, err)
}

func Test_populateLeapDataCmBadTime(t *testing.T) {
	cm := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "openshift-ptp", Name: "leap-configmap"},
		Data: map[string]string{
			"test-node-name": `# Do not edit
# This file is generated automatically by linuxptp-daemon
#$	3927775672
#@	4291747200
369221760a     37    # 1 Jan 2017`,
		},
	}
	os.Setenv("NODE_NAME", "test-node-name")
	client := fake.NewSimpleClientset(cm)
	lm := &LeapManager{
		UbloxLsInd:  make(chan ublox.TimeLs),
		Close:       make(chan bool),
		client:      client,
		namespace:   "openshift-ptp",
		retryUpdate: false,
	}
	err := lm.populateLeapData()
	assert.Error(t, err)
}

func Test_populateWithFile(t *testing.T) {
	os.Setenv("NODE_NAME", "test-node-name")
	cm := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "openshift-ptp", Name: "leap-configmap"},
	}
	client := fake.NewSimpleClientset(cm)
	lm := &LeapManager{
		UbloxLsInd:   make(chan ublox.TimeLs),
		Close:        make(chan bool),
		client:       client,
		namespace:    "openshift-ptp",
		retryUpdate:  false,
		leapFilePath: "testdata",
		leapFileName: "leap-seconds.list",
	}
	err := lm.populateLeapData()
	assert.NoError(t, err)
}

func Test_handleLeapIndication(t *testing.T) {
	cm := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "openshift-ptp", Name: "leap-configmap"},
		Data: map[string]string{
			"test-node-name": `# Do not edit
# This file is generated automatically by linuxptp-daemon
#$	3927775672
#@	4291747200
3692217600     37    # 1 Jan 2017`,
		},
	}
	os.Setenv("NODE_NAME", "test-node-name")
	client := fake.NewSimpleClientset(cm)
	lm := &LeapManager{
		UbloxLsInd:  make(chan ublox.TimeLs),
		Close:       make(chan bool),
		client:      client,
		namespace:   "openshift-ptp",
		retryUpdate: false,
	}
	err := lm.populateLeapData()
	assert.NoError(t, err)
	assert.Equal(t, 1, len(lm.leapFile.LeapEvents))
	data := &ublox.TimeLs{
		SrcOfCurrLs:   2,
		CurrLs:        18,
		SrcOfLsChange: 2,
		LsChange:      1,
		TimeToLsEvent: 10000,
		DateOfLsGpsWn: 2321,
		DateOfLsGpsDn: 1,
		Valid:         3,
	}
	lm.handleLeapIndication(data)
	assert.Equal(t, 2, len(lm.leapFile.LeapEvents))
	assert.Equal(t, 38, lm.leapFile.LeapEvents[1].LeapSec)
	assert.Equal(t, "3928780800", lm.leapFile.LeapEvents[1].LeapTime)
	assert.Equal(t, "4291747200", lm.leapFile.ExpirationTime)
}
