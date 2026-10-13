package main

import (
	"context"
	"flag"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/cep"
)

var GitCommit = "Undefined"

var (
	socket    string
	port      int
	storePath string
	nodeName  string
	tlsCert   string
	tlsKey    string
)

func main() {
	flag.StringVar(&socket, "socket", "/var/run/ptp/ipc.sock", "Path to daemon IPC Unix socket.")
	flag.IntVar(&port, "api-port", 9043, "The port the REST API endpoint binds to.")
	flag.StringVar(&storePath, "store-path", "/var/run/ptp", "Directory for persistent subscription storage.")
	flag.StringVar(&tlsCert, "tls-cert", "", "Path to TLS certificate file. If set with --tls-key, the server uses TLS.")
	flag.StringVar(&tlsKey, "tls-key", "", "Path to TLS private key file. If set with --tls-cert, the server uses TLS.")
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

	glog.Infof("cloud-event-proxy starting, node=%s, socket=%s, commit=%s", nodeName, socket, GitCommit)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	defer stop()

	cache := cep.NewEventCache(nodeName)
	ps := cep.NewPubSub(filepath.Join(storePath, "subscriptions.json"), cep.NewHTTPWriterFunc(2*time.Second))
	if err := ps.LoadFromDisk(); err != nil {
		glog.Errorf("failed to load subscriptions from %s: %v", storePath, err)
	}
	proxy := cep.NewCloudEventProxy(cache, ps)

	if err := os.MkdirAll(filepath.Dir(socket), 0755); err != nil {
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
