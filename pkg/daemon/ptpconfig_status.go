package daemon

import (
	"context"
	"fmt"
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
	i := strings.Index(profileName, ProfileNameSeparator)
	if i < 0 {
		return "", profileName, false
	}
	return profileName[:i], profileName[i+1:], true
}

// reportPluginStatus reports hardware plugin configuration status to the PtpConfig CRD.
// On success it sets HardwarePluginReady=True; on errors it sets HardwarePluginReady=False
// with the error details in the condition message.
func (dn *Daemon) reportPluginStatus(profileName string, pluginErrors []error) {
	if dn.ptpClient == nil {
		if len(pluginErrors) > 0 {
			glog.Warningf("ptpClient is nil, cannot update PtpConfig status for plugin errors: %v", pluginErrors)
		}
		return
	}

	if len(pluginErrors) == 0 {
		configName, originalProfileName, found := FindPtpConfigByProfileName(profileName)
		if found {
			UpdatePtpConfigCondition(dn.ptpClient, configName,
				ConditionTypeHardwarePluginReady,
				metav1.ConditionTrue,
				"HardwarePluginConfigured",
				fmt.Sprintf("Hardware plugin configured successfully for profile %s on node %s", originalProfileName, dn.nodeName),
			)
		}
		return
	}

	configName, originalProfileName, found := FindPtpConfigByProfileName(profileName)
	if !found {
		glog.Warningf("Could not find PtpConfig for profile %s to report plugin errors: %v", originalProfileName, pluginErrors)
		return
	}

	glog.Warningf("Hardware plugin errors for profile %s: %v", originalProfileName, pluginErrors)
	var msgs []string
	for _, e := range pluginErrors {
		msgs = append(msgs, e.Error())
	}
	UpdatePtpConfigCondition(dn.ptpClient, configName,
		ConditionTypeHardwarePluginReady,
		metav1.ConditionFalse,
		"HardwarePluginConfigError",
		fmt.Sprintf("Hardware plugin configuration errors on node %s for profile %s: %s",
			dn.nodeName, originalProfileName, strings.Join(msgs, "; ")),
	)
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
