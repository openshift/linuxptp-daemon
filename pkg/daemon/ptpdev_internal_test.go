package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clockmgr"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	ptpclient "github.com/k8snetworkplumbingwg/ptp-operator/pkg/client/clientset/versioned"
	ptpscheme "github.com/k8snetworkplumbingwg/ptp-operator/pkg/client/clientset/versioned/scheme"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

const testNodeName = "test-node"

func Test_reportedClockType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		proc *ptpProcess
		want string
	}{
		{
			name: "nil process",
			proc: nil,
			want: "",
		},
		{
			name: "ptpSettings clockType T-GM",
			proc: &ptpProcess{
				clockType: event.GM,
				nodeProfile: ptpv1.PtpProfile{
					PtpSettings: map[string]string{"clockType": TGM},
				},
			},
			want: TGM,
		},
		{
			name: "ptpSettings clockType T-BC",
			proc: &ptpProcess{
				clockType: event.BC,
				nodeProfile: ptpv1.PtpProfile{
					PtpSettings: map[string]string{"clockType": TBC},
				},
			},
			want: TBC,
		},
		{
			name: "inferred GM is reported as T-GM",
			proc: &ptpProcess{
				clockType:   event.GM,
				nodeProfile: ptpv1.PtpProfile{},
			},
			want: TGM,
		},
		{
			name: "inferred BC",
			proc: &ptpProcess{
				clockType:   event.BC,
				nodeProfile: ptpv1.PtpProfile{},
			},
			want: string(event.BC),
		},
		{
			name: "inferred OC",
			proc: &ptpProcess{
				clockType:   event.OC,
				nodeProfile: ptpv1.PtpProfile{},
			},
			want: string(event.OC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, reportedClockType(tt.proc))
		})
	}
}

func Test_setCondition(t *testing.T) {
	t.Parallel()

	first := metav1.NewTime(time.Unix(100, 0).UTC())
	second := metav1.NewTime(time.Unix(200, 0).UTC())

	t.Run("inserts when missing", func(t *testing.T) {
		t.Parallel()
		conds := []metav1.Condition{}
		setCondition(&conds, metav1.Condition{
			Type:               conditionReady,
			Status:             metav1.ConditionTrue,
			LastTransitionTime: first,
			Reason:             reasonProfileApplied,
		})
		assert.Len(t, conds, 1)
		assert.Equal(t, conditionReady, conds[0].Type)
		assert.Equal(t, metav1.ConditionTrue, conds[0].Status)
		assert.True(t, conds[0].LastTransitionTime.Equal(&first))
	})

	t.Run("preserves LastTransitionTime when status is unchanged", func(t *testing.T) {
		t.Parallel()
		conds := []metav1.Condition{{
			Type:               conditionReady,
			Status:             metav1.ConditionTrue,
			LastTransitionTime: first,
			Reason:             reasonProfileApplied,
		}}
		setCondition(&conds, metav1.Condition{
			Type:               conditionReady,
			Status:             metav1.ConditionTrue,
			LastTransitionTime: second,
			Reason:             reasonProfileApplied,
			Message:            "updated message",
		})
		assert.Len(t, conds, 1)
		assert.Equal(t, "updated message", conds[0].Message)
		assert.True(t, conds[0].LastTransitionTime.Equal(&first))
	})

	t.Run("updates LastTransitionTime when status changes", func(t *testing.T) {
		t.Parallel()
		conds := []metav1.Condition{{
			Type:               conditionReady,
			Status:             metav1.ConditionFalse,
			LastTransitionTime: first,
			Reason:             reasonNoProfile,
		}}
		setCondition(&conds, metav1.Condition{
			Type:               conditionReady,
			Status:             metav1.ConditionTrue,
			LastTransitionTime: second,
			Reason:             reasonProfileApplied,
		})
		assert.Len(t, conds, 1)
		assert.Equal(t, metav1.ConditionTrue, conds[0].Status)
		assert.True(t, conds[0].LastTransitionTime.Equal(&second))
	})
}

func Test_removeCondition(t *testing.T) {
	t.Parallel()

	t.Run("removes matching type", func(t *testing.T) {
		t.Parallel()
		conds := []metav1.Condition{
			{Type: conditionReady, Status: metav1.ConditionTrue},
			{Type: conditionSynced, Status: metav1.ConditionFalse},
		}
		removeCondition(&conds, conditionSynced)
		assert.Len(t, conds, 1)
		assert.Equal(t, conditionReady, conds[0].Type)
	})

	t.Run("no-op when type is missing", func(t *testing.T) {
		t.Parallel()
		conds := []metav1.Condition{{Type: conditionReady, Status: metav1.ConditionTrue}}
		removeCondition(&conds, conditionSynced)
		assert.Len(t, conds, 1)
	})
}

func Test_runSyncStatusUpdate_nilClient(t *testing.T) {
	t.Parallel()
	dn := &Daemon{
		nodeName:       testNodeName,
		processManager: &ProcessManager{},
	}
	dn.runSyncStatusUpdate()
}

func Test_runSyncStatusUpdate_skipsWhileApplying(t *testing.T) {
	t.Parallel()
	profile := "grandmaster"
	fake := &nodePtpDeviceFake{device: newTestNodePtpDevice(nil, nil)}
	mgr := clockmgr.Init("test-node", make(chan event.Event, 1), nil, nil, nil, nil)
	mgr.SetApplying(true)
	dn := &Daemon{
		nodeName:  testNodeName,
		ptpClient: newTestPTPClient(t, fake),
		processManager: &ProcessManager{
			clockMgr: mgr,
			process: []*ptpProcess{{
				clockType:   event.GM,
				nodeProfile: ptpv1.PtpProfile{Name: &profile},
			}},
		},
	}

	dn.runSyncStatusUpdate()
	assert.Equal(t, 0, fake.putCount)
	assert.Nil(t, fake.updated())

	dn.doSyncStatusUpdate(true)
	assert.Equal(t, 1, fake.putCount)
	assert.NotNil(t, fake.updated())
}

func Test_runSyncStatusUpdate(t *testing.T) {
	profileA := "grandmaster"
	profileB := "boundary"

	newProc := func(name *string, clockType event.ClockType, stopped bool, ifaces ...string) *ptpProcess {
		faces := make(config.IFaces, 0, len(ifaces))
		for _, iface := range ifaces {
			faces = append(faces, config.Iface{Name: iface})
		}
		return &ptpProcess{
			clockType:   clockType,
			stopped:     stopped,
			ifaces:      faces,
			nodeProfile: ptpv1.PtpProfile{Name: name},
		}
	}

	t.Run("writes Ready True, profiles, device backfill, and drops Synced", func(t *testing.T) {
		fake := &nodePtpDeviceFake{device: newTestNodePtpDevice([]ptpv1.PtpDevice{
			{Name: "ens1f0"},
			{Name: "ens1f1"},
			{Name: "unrelated"},
		}, []metav1.Condition{
			{Type: conditionSynced, Status: metav1.ConditionTrue, Reason: "Legacy"},
			{Type: conditionReady, Status: metav1.ConditionFalse, Reason: reasonNoProfile},
		})}
		dn := &Daemon{
			nodeName:  testNodeName,
			ptpClient: newTestPTPClient(t, fake),
			processManager: &ProcessManager{
				process: []*ptpProcess{
					nil,
					newProc(&profileA, event.GM, false, "ens1f0", ""),
					newProc(&profileA, event.GM, false, "ens1f1"),
					newProc(nil, event.OC, false),
				},
			},
		}

		dn.runSyncStatusUpdate()

		got := fake.updated()
		if !assert.NotNil(t, got) || !assert.NotNil(t, got.Status.Sync) {
			return
		}
		assert.Equal(t, []ptpv1.NodeProfileStatus{{Name: profileA, ClockType: TGM}}, got.Status.Sync.Profiles)
		assert.Equal(t, profileA, got.Status.Devices[0].Profile)
		assert.Equal(t, profileA, got.Status.Devices[1].Profile)
		assert.Empty(t, got.Status.Devices[2].Profile)

		ready := conditionByType(got.Status.Conditions, conditionReady)
		if assert.NotNil(t, ready) {
			assert.Equal(t, metav1.ConditionTrue, ready.Status)
			assert.Equal(t, reasonProfileApplied, ready.Reason)
		}
		assert.Nil(t, conditionByType(got.Status.Conditions, conditionSynced))
		assert.Equal(t, 1, fake.putCount)
	})

	t.Run("Ready False when no profile is applied", func(t *testing.T) {
		fake := &nodePtpDeviceFake{device: newTestNodePtpDevice(nil, nil)}
		dn := &Daemon{
			nodeName:  testNodeName,
			ptpClient: newTestPTPClient(t, fake),
			processManager: &ProcessManager{
				process: []*ptpProcess{newProc(nil, event.OC, false)},
			},
		}

		dn.runSyncStatusUpdate()

		got := fake.updated()
		if !assert.NotNil(t, got) {
			return
		}
		ready := conditionByType(got.Status.Conditions, conditionReady)
		if assert.NotNil(t, ready) {
			assert.Equal(t, metav1.ConditionFalse, ready.Status)
			assert.Equal(t, reasonNoProfile, ready.Reason)
		}
	})

	t.Run("Ready False ProcessesDown when profile exists but processes are stopped", func(t *testing.T) {
		fake := &nodePtpDeviceFake{device: newTestNodePtpDevice(nil, nil)}
		dn := &Daemon{
			nodeName:  testNodeName,
			ptpClient: newTestPTPClient(t, fake),
			processManager: &ProcessManager{
				process: []*ptpProcess{newProc(&profileB, event.BC, true, "ens2f0")},
			},
		}

		dn.runSyncStatusUpdate()

		got := fake.updated()
		if !assert.NotNil(t, got) || !assert.NotNil(t, got.Status.Sync) {
			return
		}
		assert.Equal(t, []ptpv1.NodeProfileStatus{{Name: profileB, ClockType: string(event.BC)}}, got.Status.Sync.Profiles)
		ready := conditionByType(got.Status.Conditions, conditionReady)
		if assert.NotNil(t, ready) {
			assert.Equal(t, metav1.ConditionFalse, ready.Status)
			assert.Equal(t, reasonProcessesDown, ready.Reason)
		}
	})

	t.Run("returns on Get failure without updating", func(t *testing.T) {
		fake := &nodePtpDeviceFake{getStatus: http.StatusNotFound}
		dn := &Daemon{
			nodeName:       testNodeName,
			ptpClient:      newTestPTPClient(t, fake),
			processManager: &ProcessManager{},
		}

		dn.runSyncStatusUpdate()
		assert.Equal(t, 0, fake.putCount)
		assert.Nil(t, fake.updated())
	})

	t.Run("retries UpdateStatus conflict then succeeds", func(t *testing.T) {
		fake := &nodePtpDeviceFake{
			device:      newTestNodePtpDevice(nil, nil),
			putStatuses: []int{http.StatusConflict},
		}
		dn := &Daemon{
			nodeName:  testNodeName,
			ptpClient: newTestPTPClient(t, fake),
			processManager: &ProcessManager{
				process: []*ptpProcess{newProc(&profileA, event.GM, false, "ens1f0")},
			},
		}

		dn.runSyncStatusUpdate()
		assert.Equal(t, 2, fake.putCount)
		assert.NotNil(t, fake.updated())
	})

	t.Run("gives up after conflict retries are exhausted", func(t *testing.T) {
		fake := &nodePtpDeviceFake{
			device:      newTestNodePtpDevice(nil, nil),
			putStatuses: []int{http.StatusConflict, http.StatusConflict, http.StatusConflict},
		}
		dn := &Daemon{
			nodeName:       testNodeName,
			ptpClient:      newTestPTPClient(t, fake),
			processManager: &ProcessManager{},
		}

		dn.runSyncStatusUpdate()
		assert.Equal(t, 3, fake.putCount)
		assert.Nil(t, fake.updated())
	})

	t.Run("returns on non-conflict UpdateStatus error", func(t *testing.T) {
		fake := &nodePtpDeviceFake{
			device:      newTestNodePtpDevice(nil, nil),
			putStatuses: []int{http.StatusInternalServerError},
		}
		dn := &Daemon{
			nodeName:       testNodeName,
			ptpClient:      newTestPTPClient(t, fake),
			processManager: &ProcessManager{},
		}

		dn.runSyncStatusUpdate()
		assert.Equal(t, 1, fake.putCount)
		assert.Nil(t, fake.updated())
	})
}

type nodePtpDeviceFake struct {
	mu          sync.Mutex
	device      *ptpv1.NodePtpDevice
	getStatus   int
	putStatuses []int
	lastUpdate  *ptpv1.NodePtpDevice
	putCount    int
}

func (f *nodePtpDeviceFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	f.mu.Lock()
	defer f.mu.Unlock()

	switch r.Method {
	case http.MethodGet:
		if f.getStatus != 0 && f.getStatus != http.StatusOK {
			writeK8sStatus(w, f.getStatus, reasonFor(f.getStatus))
			return
		}
		if f.device == nil {
			writeK8sStatus(w, http.StatusNotFound, metav1.StatusReasonNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(f.device)
	case http.MethodPut:
		f.putCount++
		code := http.StatusOK
		if len(f.putStatuses) > 0 {
			code = f.putStatuses[0]
			f.putStatuses = f.putStatuses[1:]
		}
		var updated ptpv1.NodePtpDevice
		_ = json.NewDecoder(r.Body).Decode(&updated)
		if code != http.StatusOK {
			writeK8sStatus(w, code, reasonFor(code))
			return
		}
		copied := updated.DeepCopy()
		f.device = copied
		f.lastUpdate = copied
		_ = json.NewEncoder(w).Encode(copied)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *nodePtpDeviceFake) updated() *ptpv1.NodePtpDevice {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lastUpdate == nil {
		return nil
	}
	return f.lastUpdate.DeepCopy()
}

func writeK8sStatus(w http.ResponseWriter, code int, reason metav1.StatusReason) {
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(metav1.Status{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
		Status:   metav1.StatusFailure,
		Reason:   reason,
		Code:     int32(code),
		Message:  string(reason),
	})
}

func reasonFor(code int) metav1.StatusReason {
	switch code {
	case http.StatusConflict:
		return metav1.StatusReasonConflict
	case http.StatusNotFound:
		return metav1.StatusReasonNotFound
	default:
		return metav1.StatusReasonInternalError
	}
}

func newTestNodePtpDevice(devices []ptpv1.PtpDevice, conditions []metav1.Condition) *ptpv1.NodePtpDevice {
	return &ptpv1.NodePtpDevice{
		TypeMeta: metav1.TypeMeta{
			APIVersion: ptpv1.SchemeGroupVersion.String(),
			Kind:       "NodePtpDevice",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      testNodeName,
			Namespace: PtpNamespace,
		},
		Status: ptpv1.NodePtpDeviceStatus{
			Devices:    devices,
			Conditions: conditions,
		},
	}
}

func newTestPTPClient(t *testing.T, handler http.Handler) *ptpclient.Clientset {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := &rest.Config{
		Host:    srv.URL,
		APIPath: "/apis",
		ContentConfig: rest.ContentConfig{
			GroupVersion:         &ptpv1.SchemeGroupVersion,
			NegotiatedSerializer: ptpscheme.Codecs.WithoutConversion(),
		},
		TLSClientConfig: rest.TLSClientConfig{Insecure: true},
	}
	client, err := ptpclient.NewForConfig(cfg)
	assert.NoError(t, err)
	return client
}

func conditionByType(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}
