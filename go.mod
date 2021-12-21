module github.com/openshift/linuxptp-daemon

go 1.15

require (
	github.com/golang/glog v1.0.0
	github.com/jaypipes/ghw v0.0.0-20190630182512-29869ac89830
	github.com/openshift/ptp-operator v0.0.0-20220106205412-96d436081c85
	github.com/prometheus/client_golang v1.11.0
	k8s.io/apimachinery v0.23.0
	k8s.io/client-go v11.0.0+incompatible
)

// Manually pinned to kubernetes-1.21.2
replace (
	github.com/go-logr/logr => github.com/go-logr/logr v0.4.0
	k8s.io/api => k8s.io/api v0.21.2
	k8s.io/apimachinery => k8s.io/apimachinery v0.21.2
	k8s.io/client-go => k8s.io/client-go v0.21.2
	k8s.io/component-base => k8s.io/component-base v0.21.2
	k8s.io/klog/v2 => k8s.io/klog/v2 v2.8.0
	sigs.k8s.io/controller-runtime => sigs.k8s.io/controller-runtime v0.9.2
)

replace (
	github.com/coreos/prometheus-operator => github.com/coreos/prometheus-operator v0.29.0
	github.com/gogo/protobuf => github.com/gogo/protobuf v1.3.2
	// Pinned to v2.9.2 (kubernetes-1.13.1) so https://proxy.golang.org can
	// resolve it correctly.
	github.com/prometheus/prometheus => github.com/prometheus/prometheus v0.0.0-20190424153033-d3245f150225
	k8s.io/kube-state-metrics => k8s.io/kube-state-metrics v1.6.0
)

replace github.com/operator-framework/operator-sdk => github.com/operator-framework/operator-sdk v0.10.0

replace bitbucket.org/ww/goautoneg => github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822
