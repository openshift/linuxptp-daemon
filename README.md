# Linuxptp Daemon for Kubernetes
## Table of Contents

- [linuxptp Daemon](#linuxptp-daemon)
- [Quick Start](#quick-start)

## linuxptp-daemon
linuxptp-daemon runs as a Kubernetes DaemonSet and manages linuxptp processes (ptp4l, phc2sys, timemaster).
It mounts `linuxptp-configmap` which contains aggregated ptp configurations and applies specific config for each node.
Both linuxptp-daemon and linuxptp-configmap are created in the `openshift-ptp` namespace.

When monitoring u-blox GNSS receivers, the daemon also evaluates the receiver's
`UBX-NAV-STATUS` spoofing detector. A reported spoofing or multiple-spoofing
state marks the GNSS source as lost, preventing a plausible but forged fix from
being used to keep the clock synchronized.

For HardwareConfig v2 GNSS matcher behavior and selector examples, see
[`doc/hardwareconfig-v2-device-selection.md`](doc/hardwareconfig-v2-device-selection.md).
When USB matching is ambiguous, daemon logs include the matching topology paths
that can be used to narrow the selector. Ethernet GNSS matchers can combine PCI
vendor/device IDs with interface name, PCI address, permanent MAC, and slot; an
ambiguous match reports the candidate interfaces and slots. GNSS detection
results are also reported in the HardwareConfig `status.sources` field, with the
detected TTY and a description of the matching inputs on success, or an
actionable match explanation on failure. If an explicitly configured matcher
fails to resolve, profile setup aborts rather than starting GPSD with the default
`/dev/gnss0` or another fallback device.

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

## ts2phc Poll Monitoring

The daemon explicitly tracks each configured PHC as `sink`, `source`, or
`unmanaged`. At configuration load and process restart, all known PHCs start as
sinks. Selection for synchronization makes a PHC a sink, unselection makes it
unmanaged, and explicit/default reference selection makes it a source.
Only sinks generate synthetic failure metrics for pending or missing timestamps.
Source roles are read directly from the current cycle's timestamps, without a
separate source cache. The source offset furthest from zero is reported with its
sign preserved; when no other source device has a timestamp, the daemon falls
back to the largest absolute peer difference. Offsets are in nanoseconds, and
unmeasured offsets are NaN. NaN ts2phc samples emit state-only events so FREERUN
is propagated without converting the missing offset to an integer or inserting
it into event offset statistics. Unmanaged timestamps remain available for peer
comparisons, but unknown PHCs are not added to the role map by selection or
timestamp logs. Samples skipped as non-sinks are not reopened on reselection. Roles are per process. Timestamp batches may cross a log-second boundary: a repeated
PHC with a new hardware timestamp identifies the next PPS edge, with a longer
log-time gap used when that evidence is unavailable. Timeouts report unhandled
samples without closing a partial batch, so later samples from the same edge
are still accepted. Missing sinks are reported when a new edge is identified or
when a later timeout group confirms an incomplete batch; timeout-only groups
report FREERUN once per interface per group. An initial timeout before any
timestamp remains a startup warning. Both RHEL 4.4 external-PPS logs and older reference-selection
logs are supported. See [parser documentation](pkg/parser/README.md#ts2phc-poll-monitoring).

Run the parser and focused monitoring tests with:

```sh
go test ./pkg/parser
go test ./pkg/daemon -run '^TestTS2PHC(ConfiguredRoles|RoleTransitions|SourceOffsets|Selection|ReferenceSelection|Unselected|Reselection)' -count=1
```

## Test Coverage

Run the full unit suite without generating coverage files:

```sh
SKIP_GNSS_MONITORING=1 go test ./... --tags=unittests -count=1
```

Run `make coverage-gate` to compare test coverage of your branch against the upstream main branch. The script auto-detects the upstream remote and its tracking branch.

```sh
$ make coverage-gate

Base coverage (up-main): 45.4%
Current coverage:            45.4%
Difference:                  0%
✅ Coverage unchanged.
```

You can also compare against a specific branch:

```sh
$ BASE_REF=release-4.20 make coverage-gate

Base coverage (release-4.20): 40.9%
Current coverage:            45.4%
Difference:                  4.5%
🎉 Coverage increased by 4.5%, good job!
```
