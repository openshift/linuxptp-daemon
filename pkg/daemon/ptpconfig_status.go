package daemon

import (
	"context"
	"strings"

	"github.com/golang/glog"
	ptpclient "github.com/k8snetworkplumbingwg/ptp-operator/pkg/client/clientset/versioned"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// ConditionTypeHardwarePluginReady indicates whether hardware plugin configuration was applied successfully
	ConditionTypeHardwarePluginReady = "HardwarePluginReady"
	// ProfileNameSeparator is the delimiter between the PtpConfig CR name and the profile name
	ProfileNameSeparator = "_"
)

// FindPtpConfigByProfileName extracts the PtpConfig CR name and the original profile name.
func FindPtpConfigByProfileName(profileName string) (string, string, bool) {
	parts := strings.SplitN(profileName, ProfileNameSeparator, 2)
	if len(parts) < 2 {
		return "", profileName, false
	}
	return parts[0], parts[1], true
}

// UpdatePtpConfigCondition updates a condition on the given PtpConfig's status.
func UpdatePtpConfigCondition(ptpClient *ptpclient.Clientset, configName string, condType string, status metav1.ConditionStatus, reason, message string) {
	ptpConfig, err := ptpClient.PtpV1().PtpConfigs(PtpNamespace).Get(context.TODO(), configName, metav1.GetOptions{})
	if err != nil {
		glog.Errorf("Failed to get PtpConfig %s/%s for status update: %v", PtpNamespace, configName, err)
		return
	}

	newCondition := metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}

	meta.SetStatusCondition(&ptpConfig.Status.Conditions, newCondition)

	_, err = ptpClient.PtpV1().PtpConfigs(PtpNamespace).UpdateStatus(context.TODO(), ptpConfig, metav1.UpdateOptions{})
	if err != nil {
		glog.Errorf("Failed to update PtpConfig %s/%s status: %v", PtpNamespace, configName, err)
	} else {
		glog.Infof("Updated PtpConfig %s/%s condition %s=%s reason=%s", PtpNamespace, configName, condType, status, reason)
	}
}
