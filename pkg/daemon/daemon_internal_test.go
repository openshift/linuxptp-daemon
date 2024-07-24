package daemon

// This tests daemon private functions

import (
	"os"
	"strings"
	"testing"

	"github.com/bigkevmcd/go-configparser"
	"github.com/openshift/linuxptp-daemon/pkg/leap"
	ptpv1 "github.com/openshift/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/yaml"
)

func loadProfile(path string) (*ptpv1.PtpProfile, error) {
	profileData, err := os.ReadFile(path)
	if err != nil {
		return &ptpv1.PtpProfile{}, err
	}
	profile := ptpv1.PtpProfile{}
	err = yaml.Unmarshal(profileData, &profile)
	if err != nil {
		return &ptpv1.PtpProfile{}, err
	}
	return &profile, nil
}

func mkPath(t *testing.T) {
	err := os.MkdirAll("/tmp/test", os.ModePerm)
	assert.NoError(t, err)
}

func clean(t *testing.T) {
	err := os.RemoveAll("/tmp/test")
	assert.NoError(t, err)
}
func applyProfileSyncE(t *testing.T, profile *ptpv1.PtpProfile) {

	stopCh := make(<-chan struct{})
	mockLeap()
	dn := New(
		"test-node-name",
		"openshift-ptp",
		false,
		nil,
		&LinuxPTPConfUpdate{
			UpdateCh:     make(chan bool),
			NodeProfiles: []ptpv1.PtpProfile{*profile},
		},
		stopCh,
		[]string{"e810"},
		&[]ptpv1.HwConfig{},
		nil,
		make(chan bool),
		30,
	)
	assert.NotNil(t, dn)
	err := dn.applyNodePtpProfile(0, profile)
	assert.NoError(t, err)
	close(leap.LeapMgr.Close)

}

func testRequirements(t *testing.T, profile *ptpv1.PtpProfile) {

	cfg, err := configparser.NewConfigParserFromFile("/tmp/test/synce4l.0.config")
	assert.NoError(t, err)
	for _, sec := range cfg.Sections() {
		if strings.HasPrefix(sec, "[<") {
			clk, err := cfg.Get(sec, "clock_id")
			assert.NoError(t, err)
			id, found := profile.PtpSettings["test_clock_id_override"]
			if found {
				assert.NotEqual(t, id, clk)
			} else {
				assert.NotEqual(t, "0", clk)
			}
		}
	}
}
func Test_applyProfile_synce(t *testing.T) {
	testDataFiles := []string{
		"testdata/synce-profile.yaml",
		"testdata/synce-profile-dual.yaml",
		"testdata/synce-profile-custom-id.yaml",
		"testdata/synce-profile-bad-order.yaml",
		"testdata/synce-profile-no-ifaces.yaml",
	}
	for i := range len(testDataFiles) {
		mkPath(t)
		profile, err := loadProfile(testDataFiles[i])
		assert.NoError(t, err)
		applyProfileSyncE(t, profile)
		testRequirements(t, profile)
		clean(t)
	}
}

func mockLeap() error {
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
	lm, err := leap.New(client, "openshift-ptp")
	if err != nil {
		return err
	}
	go lm.Run()
	return nil
}
