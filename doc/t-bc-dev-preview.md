# Building and using T-BC / T-TSC Holdover dev preview

This repository contains a development preview of T-BC / T-TSC holdover feature on Intel E810-XXV-4T NIC. The feature requires a special `ts2phc` build that is not yet publicly available, but [included in this repository](../extra/linuxptp-4.4-1.el9_2.test72468.1.x86_64.rpm) for testing purposes only. Use on your own risk!

## Building
### Create a Dockerfile

To include the custom `ts2phc` build in the container image, the `Dockerfile` must be modified. For example:

```bash
# This is a temporary file for building the dev. preview of T-BC holdover
FROM golang:1.23.5 AS builder
WORKDIR /go/src/github.com/k8snetworkplumbingwg/linuxptp-daemon
COPY . .
RUN make clean && make

FROM quay.io/centos/centos:stream9

COPY extra/linuxptp-4.4-1.el9_2.test72468.1.x86_64.rpm /
RUN yum -y update && yum -y update glibc && yum --setopt=skip_missing_names_on_install=False -y install /linuxptp-4.4-1.el9_2.test72468.1.x86_64.rpm ethtool hwdata synce4l && yum clean all && \
	rm /linuxptp-4.4-1.el9_2.test72468.1.x86_64.rpm


RUN yum install -y gpsd-minimal
RUN yum install -y gpsd-minimal-clients

# Create symlinks for executables to match references
RUN ln -s /usr/bin/gpspipe /usr/local/bin/gpspipe
RUN ln -s /usr/sbin/gpsd /usr/local/sbin/gpsd
RUN ln -s /usr/bin/ubxtool /usr/local/bin/ubxtool


COPY --from=builder /go/src/github.com/k8snetworkplumbingwg/linuxptp-daemon/bin/ptp /usr/local/bin/

CMD ["/usr/local/bin/ptp"]
```

### Build and push the image

```bash
podman build --arch=x86_64 --no-cache -t <your pull spec> -f Dockerfile.tbc . && podman push <your pull spec>
```

### Replace the stock image

Replace the `linuxptp-daemon` stock image by your custom image (the exact method depends on the environment used)

## Configure

A Dual-NIC T-BC configuration used for the development is shown below. The explanations and modification options will follow.

```yaml
apiVersion: ptp.openshift.io/v1
kind: PtpConfig
metadata:
  name: boundary
  namespace: openshift-ptp
spec:
  profile:
  - name: "boundary"
    plugins:
      e810:
        enableDefaultConfig: false
        interconnections:
        - id: ens7f0
          part: E810-XXVDA4T
          gnssInput: false
          upstreamPort: ens7f1
          phaseOutputConnectors:
          - SMA1
        - id: ens2f0
          part: E810-XXVDA4T
          inputConnector:
            connector: SMA1
            delayPs: 0
        pins:
          # Slot 7 outputs 1PPS from SMA1 to slot2
          ens7f0:
            SMA1: 2 1
            SMA2: 2 2
            U.FL1: 0 1
            U.FL2: 0 2
          ens2f0:
            SMA1: 1 1
            SMA2: 1 2
            U.FL1: 0 1
            U.FL2: 0 2
        settings:
          LocalHoldoverTimeout: 14400
          LocalMaxHoldoverOffSet: 1500
          MaxInSpecOffset: 100
    ptp4lOpts: "-2 --summary_interval -4"
    phc2sysOpts: "-a -r -n 24 -N 8 -R 16 -u 0"
    ts2phcConf: |
      [global]
      use_syslog  0
      verbose 1
      logging_level 7
      ts2phc.pulsewidth 100000000
      leapfile  /usr/share/zoneinfo/leap-seconds.list
      domainNumber 24
      uds_address /var/run/ptp4l.0.socket
      [ens7f0]
      ts2phc.extts_polarity rising
      ts2phc.extts_correction 0
      ts2phc.master 0
      [ens2f0]
      ts2phc.extts_polarity rising
      ts2phc.extts_correction 0
      ts2phc.master 0
    ts2phcOpts: '-s generic -a --ts2phc.external_pps 1'
    ptpSchedulingPolicy: SCHED_FIFO
    ptpSchedulingPriority: 10
    ptpSettings:
      logReduce: "false"
    ptp4lConf: |
      # The interface name is hardware-specific
      [ens7f1]
      masterOnly 0
      [ens7f0]
      masterOnly 1
      [ens7f2]
      masterOnly 1
      [ens7f3]
      masterOnly 1
      [ens2f0]
      masterOnly 1
      [ens2f1]
      masterOnly 1
      [ens2f2]
      masterOnly 1
      [ens2f3]
      masterOnly 1
      [global]
      #
      # Default Data Set
      #
      twoStepFlag 1
      slaveOnly 0
      priority1 128
      priority2 128
      domainNumber 24
      #utc_offset 37
      clockClass 248
      clockAccuracy 0xFE
      offsetScaledLogVariance 0xFFFF
      free_running 0
      freq_est_interval 1
      dscp_event 0
      dscp_general 0
      dataset_comparison G.8275.x
      G.8275.defaultDS.localPriority 128
      #
      # Port Data Set
      #
      logAnnounceInterval -3
      logSyncInterval -4
      logMinDelayReqInterval -4
      logMinPdelayReqInterval -4
      announceReceiptTimeout 3
      syncReceiptTimeout 0
      delayAsymmetry 0
      fault_reset_interval -4
      neighborPropDelayThresh 20000000
      masterOnly 0
      G.8275.portDS.localPriority 128
      #
      # Run time options
      #
      assume_two_step 0
      logging_level 6
      path_trace_enabled 0
      follow_up_info 0
      hybrid_e2e 0
      inhibit_multicast_service 0
      net_sync_monitor 0
      tc_spanning_tree 0
      tx_timestamp_timeout 50
      unicast_listen 0
      unicast_master_table 0
      unicast_req_duration 3600
      use_syslog 1
      verbose 0
      summary_interval 0
      kernel_leap 1
      check_fup_sync 0
      clock_class_threshold 135
      #
      # Servo Options
      #
      pi_proportional_const 0.0
      pi_integral_const 0.0
      pi_proportional_scale 0.0
      pi_proportional_exponent -0.3
      pi_proportional_norm_max 0.7
      pi_integral_scale 0.0
      pi_integral_exponent 0.4
      pi_integral_norm_max 0.3
      step_threshold 2.0
      first_step_threshold 0.00002
      max_frequency 900000000
      clock_servo pi
      sanity_freq_limit 200000000
      ntpshm_segment 0
      #
      # Transport options
      #
      transportSpecific 0x0
      ptp_dst_mac 01:1B:19:00:00:00
      p2p_dst_mac 01:80:C2:00:00:0E
      udp_ttl 1
      udp6_scope 0x0E
      uds_address /var/run/ptp4l
      #
      # Default interface options
      #
      clock_type BC
      network_transport L2
      delay_mechanism E2E
      time_stamping hardware
      tsproc_mode filter
      delay_filter moving_median
      delay_filter_length 10
      egressLatency 0
      ingressLatency 0
      boundary_clock_jbod 1
      #
      # Clock description
      #
      productDescription ;;
      revisionData ;;
      manufacturerIdentity 00:00:00
      userDescription ;
      timeSource 0xA0
  recommend:
  - profile: "boundary"
    priority: 4
    match:
    - nodeLabel: node-role.kubernetes.io/master

```

The holdover feature is activated by configuring the hardware plugin (pins and interconnections), and in addition configuring `ts2phc` to operate in a special way.

### Configuring pins and interconnections

The profile above is for a dual-NIC chain of clocks operating as a single boundary clock.

```yaml
        interconnections:
        - id: ens7f0
          part: E810-XXVDA4T
          gnssInput: false
          upstreamPort: ens7f1
          phaseOutputConnectors:
          - SMA1
        - id: ens2f0
          part: E810-XXVDA4T
          inputConnector:
            connector: SMA1
            delayPs: 0
        pins:
          # Slot 7 outputs 1PPS from SMA1 to slot2
          ens7f0:
            SMA1: 2 1
            SMA2: 2 2
            U.FL1: 0 1
            U.FL2: 0 2
          ens2f0:
            SMA1: 1 1
            SMA2: 1 2
            U.FL1: 0 1
            U.FL2: 0 2
```
Please note that the time receiver NIC (`id: ens7f0`) and the specific TR port (`upstreamPort: ens7f1`) have to be configured in both T-BC and T-TSC configurations
The pins API has to be set as well. In the single-NIC case, disable all the pins, or enable outputs if using for 1PPS measurements. 

### Configuring ts2phc

For `ts2phc` NIC sections in the configuration file must specify `ts2phc.master 0`. The command line options indicate the "generic" time source and the external PPS option`-a --ts2phc.external_pps 1`.
To render this configuration to a single-NIC or T-TSC clock, remove the unused NIC sections from `ts2phcConf`.

```yaml
    ts2phcConf: |
      [global]
      use_syslog  0
      verbose 1
      logging_level 7
      ts2phc.pulsewidth 100000000
      leapfile  /usr/share/zoneinfo/leap-seconds.list
      domainNumber 24
      uds_address /var/run/ptp4l.0.socket
      [ens7f0]
      ts2phc.extts_polarity rising
      ts2phc.extts_correction 0
      ts2phc.master 0
      [ens2f0]
      ts2phc.extts_polarity rising
      ts2phc.extts_correction 0
      ts2phc.master 0
    ts2phcOpts: '-s generic -a --ts2phc.external_pps 1'
```

### Configuring ptp4l

Customizing ptp4l configuration is done through the interface sections in the `ptp4lConf`:

```yaml
    ptp4lConf: |
      # The interface name is hardware-specific
      [ens7f1]
      masterOnly 0
      [ens7f0]
      masterOnly 1
      [ens7f2]
      masterOnly 1
      [ens7f3]
      masterOnly 1
      [ens2f0]
      masterOnly 1
      [ens2f1]
      masterOnly 1
```
Only the TR port has `masterOnly 0`. All the TT ports must have `masterOnly 1`. In case of T-TSC, there should be only one port defined with `masterOnly 0`.

## Testing

Entering and exiting the holdover can be tested by disabling the TR port on DUT, or disabling the ptp protocol on the T-GM / T-BC connected to it. The DPLL offsets can be monitored by netlink tooling, for example:

```bash
sudo podman run --privileged --network=host --rm quay.io/vgrinber/tools:dpll dpll-cli monitor |jq -r '"\(.boardLabel)\t\(.pinParentDevice[1].phaseOffsetPs)"'
``` 