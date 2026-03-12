# Linuxptp Daemon for Kubernetes
## Table of Contents

- [linuxptp Daemon](#linuxptp-daemon)
- [Quick Start](#quick-start)

## linuxptp-daemon
linuxptp-daemon runs as a Kubernetes DaemonSet and manages linuxptp processes (ptp4l, phc2sys, timemaster).
It mounts `linuxptp-configmap` which contains aggregated ptp configurations and applies specific config for each node.
Both linuxptp-daemon and linuxptp-configmap are created in the `openshift-ptp` namespace.

## Quick Start

### Create namespace and ServiceAccount

All ptp related resources reside within the `openshift-ptp` namespace, including `linuxptp-daemon` and `linuxptp-configmap`.
```
$ kubectl create -f deploy/00-ns.yaml
$ kubectl create -f deploy/01-sa.yaml
$ kubectl create -f deploy/02-rbac.yaml
```

### Generate linuxptp-configmap data sources

`linuxptp-configmap` defines the linuxptp configuration for all nodes in the cluster. Each key of the ConfigMap's data should
equal the name of a node. The value is a JSON formatted string representing the desired linuxptp configurations(ptp4lOpts, phc2sysOpts, Interface etc).

The script below helps to generate data sources for all the nodes under `linuxptp-configmap` directory which will be
used later to create the ConfigMap.
```
$ ./hack/gen-configmap-data-source.sh

```

For example, if the cluster only has one node called `node.example.com`, then generated file would be:
```
$ ls ./linuxptp-configmap
node.example.com
```

### Customize linuxptp-configmap

The generated data sources are created within `./linuxptp-configmap/`, one file per node. Each has hard-coded linuxptp
configuration in each node file. Usually the user will need to change the config according to their own environments,
for example, changing the `Interface` name `eth0` to the PTP capable device `ens786f1` in node specific file.

```
$ cat linuxptp-configmap/node.example.com
{"interface":"eth0", "ptp4lOpts":"-s -2", "phc2sysOpts":"-a -r"}
$ sed -i 's/eth0/ens786f1/' linuxptp-configmap/node.example.com
```

### Create ptp-configmap

```
$ kubectl create configmap linuxptp-configmap --from-file ./linuxptp-configmap
$ kubectl get configmap linuxptp-configmap -o yaml -n openshift-ptp
apiVersion: v1
data:
  node.example.com: |
    {"interface":"ens786f1", "ptp4lOpts":"-s -2", "phc2sysOpts":"-a -r"}
kind: ConfigMap
metadata:
  creationTimestamp: "2019-10-10T09:03:39Z"
  name: linuxptp-configmap
  namespace: openshift-ptp
  resourceVersion: "2323998"
  selfLink: /api/v1/namespaces/openshift-ptp/configmaps/linuxptp-configmap
  uid: 40c031f4-e09d-40a8-b081-92c3b8c0accb
```

### Create linuxptp-daemon

1. Build linuxptp daemon image `openshift.io/linuxptp-daemon` used by linuxptp-daemon.yaml

```
$ make image
```

2. This deploys a linuxptp-daemon pod on each node, the daemon mounts linuxptp-configmap and configures the linuxptp
processes(ptp4l, phc2sys) according to data sources as specified for the specific node in the ConfigMap.

```
$ kubectl create -f deploy/linuxptp-daemon.yaml

$ kubectl get pods -n openshift-ptp
NAME                    READY   STATUS    RESTARTS   AGE
linuxptp-daemon-txmpn   1/1     Running   0          105m
```
