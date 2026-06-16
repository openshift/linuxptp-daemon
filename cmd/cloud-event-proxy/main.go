package main

import (
	"flag"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/cep"

	"github.com/golang/glog"
)

var (
	socket    string
	port      int
	storePath string
	nodeName  string
)

func main() {
	flag.StringVar(&socket, "socket", "/var/run/ptp/events.sock", "Path to daemon IPC Unix socket.")
	flag.IntVar(&port, "api-port", 9043, "The port the REST API endpoint binds to.")
	flag.StringVar(&storePath, "store-path", "/var/run/ptp", "Directory for persistent subscription storage.")
	flag.Parse()

	nodeName = os.Getenv("NODE_NAME")
	if nodeName == "" {
		var err error
		if nodeName, err = os.Hostname(); err != nil {
			glog.Fatalf("NODE_NAME not set and failed to get hostname: %v", err)
		}
	}

	glog.Infof("cloud-event-proxy starting, node=%s, socket=%s", nodeName, socket)

	closeCh := make(chan struct{})

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		sig := <-sigCh
		glog.Infof("received signal %s, shutting down", sig)
		close(closeCh)
	}()

	cache := cep.NewEventCache(nodeName)
	ps := cep.NewPubSub(filepath.Join(storePath, "subscriptions.json"), cep.NewHTTPWriterFunc(2*time.Second))
	if err := ps.LoadFromDisk(); err != nil {
		glog.Errorf("failed to load subscriptions from %s: %v", storePath, err)
	}
	proxy := cep.NewCloudEventProxy(cache, ps)

	os.Remove(socket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		glog.Fatalf("failed to listen on %s: %v", socket, err)
	}

	go func() {
		for {
			glog.Infof("waiting for daemon connection on %s", socket)
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				select {
				case <-closeCh:
					return
				default:
				}
				glog.Errorf("accept error: %v", acceptErr)
				continue
			}
			glog.Info("daemon connected")
			proxy.Listen(conn)
			conn.Close()
			glog.Warning("daemon disconnected")
		}
	}()

	go proxy.ListenAndServe(port)

	<-closeCh
	ln.Close()
	os.Remove(socket)
	glog.Info("cloud-event-proxy exiting")
}
