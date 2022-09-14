package daemon

import (
	"context"
	"fmt"
	"github.com/golang/glog"
	"github.com/openshift/linuxptp-daemon/pkg/config"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func createHaConfigMap() {
	cfg, err := config.GetKubeConfig()
	if err != nil {
		glog.Errorf("get kubeconfig failed: %v", err)
		return
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		panic(err)
	}

	configMapsClient := clientset.CoreV1().ConfigMaps("openshift-ptp")
	configMapHA := &apiv1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "configMapHA",
			Namespace: "openshift-ptp",
		},
		Data: nil,
	}
	configMap, err := configMapsClient.Create(context.TODO(), configMapHA, metav1.CreateOptions{})
	if err != nil {
		return
	}
	fmt.Printf("Created HA ConfigMap %1, \n", configMap.GetObjectMeta().GetName())
}
