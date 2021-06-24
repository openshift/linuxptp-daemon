package main

import (
	"flag"
	"io/ioutil"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/golang/glog"
	"k8s.io/client-go/kubernetes"

	"github.com/openshift/linuxptp-daemon/pkg/config"
	"github.com/openshift/linuxptp-daemon/pkg/daemon"
	ptpclient "github.com/openshift/ptp-operator/pkg/client/clientset/versioned"
)

type cliParams struct {
	updateInterval int
	profileDir     string
}

const ChronydSvcName = "chronyd"

// Parse Command line flags
func flagInit(cp *cliParams) {
	flag.IntVar(&cp.updateInterval, "update-interval", config.DefaultUpdateInterval,
		"Interval to update PTP status")
	flag.StringVar(&cp.profileDir, "linuxptp-profile-path", config.DefaultProfilePath,
		"profile to start linuxptp processes")
}

func isServiceActive(serviceName string) bool {
	// this container need to have a local volume /host mounted on host /
	cmd := exec.Command("chroot", "/host", "systemctl", "is-active", serviceName)
	status, err := cmd.CombinedOutput()
	if err != nil {
		glog.Infof("service %s is-active check returned with: %v", serviceName, err)
		return false
	}
	if strings.TrimSpace(string(status)) == "active" {
		return true
	}
	return false
}

// start or stop a service. action: ["start", "stop"]
func setService(action, serviceName string) {
	glog.Infof("%s service %s", action, serviceName)
	cmd := exec.Command("chroot", "/host", "systemctl", action, serviceName)
	status, err := cmd.CombinedOutput()
	if err != nil {
		glog.Errorf("Failed to %s service %s: %v", action, serviceName, status)
	}
}

// stopChronyCh: when ptp is started; recoverChronyCh: when main process exit; done: ack to main
func manageChronyd(stopChronyCh, recoverChronyCh, done chan struct{}) {
	initialState := isServiceActive(ChronydSvcName)
	currentState := initialState
	for {
		select {
		case <-stopChronyCh:
			if currentState == true {
				setService("stop", ChronydSvcName)
				currentState = false
			}
		case <-recoverChronyCh:
			if currentState == false && initialState == true {
				setService("start", ChronydSvcName)
				currentState = true
			}
			done <- struct{}{}
		}
	}
}

func main() {
	cp := &cliParams{}
	flag.Parse()
	flagInit(cp)

	glog.Infof("resync period set to: %d [s]", cp.updateInterval)
	glog.Infof("linuxptp profile path set to: %s", cp.profileDir)

	cfg, err := config.GetKubeConfig()
	if err != nil {
		glog.Errorf("get kubeconfig failed: %v", err)
		return
	}
	glog.Infof("successfully get kubeconfig")

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		glog.Errorf("cannot create new config for kubeClient: %v", err)
		return
	}

	ptpClient, err := ptpclient.NewForConfig(cfg)
	if err != nil {
		glog.Errorf("cannot create new config for ptpClient: %v", err)
		return
	}

	// The name of NodePtpDevice CR for this node is equal to the node name
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		glog.Error("cannot find NODE_NAME environment variable")
		return
	}

	// Run a loop to update the device status
	go daemon.RunDeviceStatusUpdate(ptpClient, nodeName)

	stopCh := make(chan struct{})
	defer close(stopCh)

	stopChronydCh := make(chan struct{})
	defer close(stopChronydCh)

	recoverChronydCh := make(chan struct{})
	defer close(recoverChronydCh)

	doneChronyCh := make(chan struct{})
	defer close(doneChronyCh)

	go manageChronyd(stopChronydCh, recoverChronydCh, doneChronyCh)

	ptpConfUpdate, err := daemon.NewLinuxPTPConfUpdate()
	if err != nil {
		glog.Errorf("failed to create a ptp config update: %v", err)
		return
	}

	go daemon.New(
		nodeName,
		daemon.PtpNamespace,
		kubeClient,
		ptpConfUpdate,
		stopCh,
		stopChronydCh,
	).Run()

	tickerPull := time.NewTicker(time.Second * time.Duration(cp.updateInterval))
	defer tickerPull.Stop()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	daemon.StartMetricsServer("0.0.0.0:9091")

	for {
		select {
		case <-tickerPull.C:
			glog.Infof("ticker pull")
			nodeProfile := filepath.Join(cp.profileDir, nodeName)
			if _, err := os.Stat(nodeProfile); err != nil {
				if os.IsNotExist(err) {
					glog.Infof("ptp profile doesn't exist for node: %v", nodeName)
					continue
				} else {
					glog.Errorf("error stating node profile %v: %v", nodeName, err)
					continue
				}
			}
			nodeProfilesJson, err := ioutil.ReadFile(nodeProfile)
			if err != nil {
				glog.Errorf("error reading node profile: %v", nodeProfile)
				continue
			}

			err = ptpConfUpdate.UpdateConfig(nodeProfilesJson)
			if err != nil {
				glog.Errorf("error updating the node configuration using the profiles loaded: %v", err)
			}
		case sig := <-sigCh:
			glog.Info("signal received, shutting down", sig)
			recoverChronydCh <- struct{}{}
			<-doneChronyCh
			return
		}
	}
}
