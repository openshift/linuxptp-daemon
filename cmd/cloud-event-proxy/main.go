package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/cep"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"k8s.io/client-go/kubernetes"
)

var GitCommit = "Undefined"

var (
	socket    string
	port      int
	storePath string
	nodeName  string
	apiHost   string
	tlsCert   string
	tlsKey    string
)

func main() {
	flag.StringVar(&socket, "socket", "/var/run/ptp/ipc.sock", "Path to daemon IPC Unix socket.")
	flag.IntVar(&port, "api-port", 9043, "The port the REST API endpoint binds to.")
	flag.StringVar(&storePath, "store-path", "", "Directory for persistent subscription storage."+
		"This will cause subscriptions to be persisted to a disk backend, instead of the default ConfigMap backend.")
	flag.StringVar(&tlsCert, "tls-cert", "", "Path to TLS certificate file. If set with --tls-key, the server uses TLS.")
	flag.StringVar(&tlsKey, "tls-key", "", "Path to TLS private key file. If set with --tls-cert, the server uses TLS.")
	flag.StringVar(&apiHost, "api-host", "", "Host used to build subscription UriLocation values in API responses. "+
		"Defaults to the node name.")
	flag.Parse()

	if tlsKey != "" || tlsCert != "" {
		if tlsKey == "" || tlsCert == "" {
			glog.Fatal("if one of the two is provided, both --tls-key and --tls-cert must be set")
		}
	}

	nodeName = os.Getenv("NODE_NAME")
	if nodeName == "" {
		var err error
		if nodeName, err = os.Hostname(); err != nil {
			glog.Fatalf("NODE_NAME not set and failed to get hostname: %v", err)
		}
	}

	if apiHost == "" {
		apiHost = nodeName
	}
	scheme := "http"
	if tlsCert != "" {
		scheme = "https"
	}
	// baseURL is the stable, server-determined root used to build subscription UriLocation values.
	baseURL := fmt.Sprintf("%s://%s:%d", scheme, apiHost, port)

	glog.Infof("cloud-event-proxy starting, node=%s, socket=%s, baseURL=%s, commit=%s", nodeName, socket, baseURL, GitCommit)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	defer stop()

	cache := cep.NewEventCache(nodeName)
	var storage cep.Store
	var err error
	if storePath != "" {
		path := filepath.Join(storePath, "subscriptions.json")
		storage, err = cep.NewDiskStore(path)
		if err != nil {
			glog.Fatalf("failed to create disk store at path %s: %v", path, err)
		}
	} else {
		storage, err = initConfigMapStore(nodeName)
		if err != nil {
			glog.Fatalf("failed to init ConfigMap subscription store: %v", err)
		}
	}
	ps := cep.NewPubSub(storage, cep.NewHTTPWriterFunc(2*time.Second))
	if err = ps.LoadFromStore(ctx); err != nil {
		glog.Fatalf("failed to load persisted subscriptions: %v", err)
	}
	proxy := cep.NewCloudEventProxy(cache, ps, baseURL)

	if err = os.MkdirAll(filepath.Dir(socket), 0755); err != nil {
		glog.Fatalf("failed to create socket directory: %v", err)
	}
	// Remove any socket left behind by a previous unclean exit
	if err = os.Remove(socket); err != nil && !os.IsNotExist(err) {
		glog.Fatalf("failed to remove stale socket %s: %v", socket, err)
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		glog.Fatalf("failed to listen on %s: %v", socket, err)
	}
	defer ln.Close()

	go proxy.AcceptDaemonConnections(ctx, ln)

	if err = proxy.ListenAndServe(ctx, port, tlsCert, tlsKey); err != nil {
		glog.Fatalf("API server error: %v", err)
	}
}

func resolveNamespace() (string, error) {
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns, nil
	}
	return "", fmt.Errorf("POD_NAMESPACE not set")
}

func initConfigMapStore(nodeName string) (cep.Store, error) {
	ns, err := resolveNamespace()
	if err != nil {
		return nil, fmt.Errorf("error resolving namespace for ConfigMap store: %w", err)
	}
	cfg, err := config.GetKubeConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to build kube config: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}
	storage, err := cep.NewConfigMapStore(context.TODO(), clientset, ns, nodeName)
	if err != nil {
		return nil, fmt.Errorf("error creating ConfigMap store: %w", err)
	}
	return storage, nil
}
