# Configuring T-GM on Dell XR8720t platform

This document describes how to configure and operate T-GM with holdover support on the Dell XR8720t platform.

## How it works

In the T-GM configuration linuxptp-daemon will launch a `ptp4l` instance for all the TT ports. The internal NAC that receives the GNSS signal leads the timing of the entire system. With two add-on follower cards, this configuration supports up to 24 TT ports.

The `ts2phc` will synchronize the physical hardware clocks (PHCs) to the GNSS time source using NMEA and 1PPS signals. If the GNSS signal is lost, the system will enter holdover.

## Configure

A Triple-NIC T-GM configuration example is shown below.
The `PtpConfig` resource contains a single profile (`grandmaster`) configuring the TT ports, `ts2phc`, and `phc2sys`.

In addition, a `HardwareConfig` resource is required to configure the hardware.

The system also requires a `MachineConfig` resource to configure the network interface names according to the `path` name policy.

### PtpConfig

```yaml
apiVersion: ptp.openshift.io/v1
kind: PtpConfig
metadata:
  name: gnrd-tgm
  namespace: openshift-ptp
spec:
  profile:
    - name: "grandmaster"
      ptp4lOpts: "-2 --summary_interval -4"
      phc2sysOpts: "-r -u 0 -m -N 8 -R 16 -n 24 -s eno8703"
      ptpSchedulingPolicy: SCHED_FIFO
      ptpSchedulingPriority: 10
      ptpSettings:
        logReduce: "enhanced"
      ts2phcOpts: "-m"
      ts2phcConf: |
        [nmea]
        ts2phc.master 1
        [global]
        use_syslog  0
        verbose 1
        logging_level 7
        ts2phc.pulsewidth 100000000
        ts2phc.nmea_serialport autodetect
        [eno8703]
        ts2phc.extts_polarity rising
        ts2phc.extts_correction 0
        ts2phc.master  0
        ts2phc.channel 0
        ts2phc.pin_index 1
        [enp108s0f0]
        ts2phc.extts_polarity   rising
        ts2phc.extts_correction 0
        ts2phc.master   0
        ts2phc.channel 0
        ts2phc.pin_index 1
        [enp110s0f0]
        ts2phc.extts_polarity   rising
        ts2phc.extts_correction 0
        ts2phc.master   0
        ts2phc.channel 0
        ts2phc.pin_index 1
      ptp4lConf: |
        #[eno8303]
        # ** Host management interface
        [eno8403]
        masterOnly 1
        [eno8503]
        masterOnly 1
        [eno8603]
        masterOnly 1
        [eno8703]
        masterOnly 1
        [eno8803]
        masterOnly 1
        [eno8903]
        masterOnly 1
        [eno9003]
        masterOnly 1
        [enp108s0f0]
        masterOnly 1
        [enp108s0f1]
        masterOnly 1
        [enp108s0f2]
        masterOnly 1
        [enp108s0f3]
        masterOnly 1
        [enp108s0f4]
        masterOnly 1
        [enp108s0f5]
        masterOnly 1
        [enp108s0f6]
        masterOnly 1
        [enp108s0f7]
        masterOnly 1
        [enp110s0f0]
        masterOnly 1
        [enp110s0f1]
        masterOnly 1
        [enp110s0f2]
        masterOnly 1
        [enp110s0f3]
        masterOnly 1
        [enp110s0f4]
        masterOnly 1
        [enp110s0f5]
        masterOnly 1
        [enp110s0f6]
        masterOnly 1
        [enp110s0f7]
        masterOnly 1
        [global]
        #
        # Default Data Set
        #
        twoStepFlag 1
        priority1 128
        priority2 128
        domainNumber 24
        #utc_offset 37
        clockClass 6
        clockAccuracy 0x27
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
        logMinPdelayReqInterval 0
        announceReceiptTimeout 3
        syncReceiptTimeout 0
        delayAsymmetry 0
        fault_reset_interval 4
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
        summary_interval -4
        kernel_leap 1
        check_fup_sync 0
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
        step_threshold 0.0
        first_step_threshold 0.00002
        clock_servo pi
        sanity_freq_limit  200000000
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
        timeSource 0x20
  recommend:
    - profile: "grandmaster"
      priority: 4
      match:
        - nodeLabel: "node-role.kubernetes.io/worker"
```

### HardwareConfig

```yaml
apiVersion: ptp.openshift.io/v2alpha1
kind: HardwareConfig
metadata:
  name: gnrd-tgm
  namespace: openshift-ptp
spec:
  profile:
    name: gnrd-tgm
    clockType: T-GM
    clockChain:
      structure:
        - name: nac
          hardwareSpecificDefinitions: dell/XR8720t
          dpll:
            holdoverParameters:
              maxInSpecOffset: 100
              localMaxHoldoverOffset: 100
              # This is a 4hr holdover.  Set to 28800 for 8hr
              localHoldoverTimeout: 14400
        - name: cf1
          hardwareSpecificDefinitions: intel/e830
          dpll:
            networkInterface: enp108s0f0
        - name: cf2
          hardwareSpecificDefinitions: intel/e830
          dpll:
            networkInterface: enp110s0f0
      behavior:
        sources:
          - name: GNSS
            sourceType: gnss
            subsystem: nac
            gnssConfig:
              init:
                antennaVoltage: true
                constellations:
                  - GPS
                  - Galileo
                survey:
                  observationTime: 86400
                  accuracy: 1
  relatedPtpProfileName: grandmaster
```

### Network interface names

```yaml
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  name: 10-rename-gnrd-interfaces-master
  labels:
    machineconfiguration.openshift.io/role: master
spec:
  config:
    ignition:
      version: 3.2.0
    storage:
      files:
        - path: /etc/systemd/network/10-interface-8086-12d3.link
          # [Match]
          # Property=ID_VENDOR_ID=0x8086
          # Property=ID_MODEL_ID=0x12d3
          # [Link]
          # NamePolicy=path
          mode: 420
          overwrite: true
          contents:
            source: data:text/plain,%5BMatch%5D%0AProperty%3DID_VENDOR_ID%3D0x8086%0AProperty%3DID_MODEL_ID%3D0x12d3%0A%0A%5BLink%5D%0ANamePolicy%3Dpath%0A
```
