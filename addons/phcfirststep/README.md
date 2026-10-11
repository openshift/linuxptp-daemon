# PHC First-Step Plugin

`phc-first-step` synchronously applies the initial PHC correction for a
time-receiver profile before normal daemon profile application continues.
The operator enables the plugin by default, but a profile runs it only when
its `plugins` map contains `phc-first-step`.

The plugin runs free-running `ptp4l` over the profile's time-receiver
(upstream) ports, taken from the `ptp4lConf` interface sections with
`masterOnly 0`, until it receives one valid master-offset sample with non-zero
path delay. It then reads and sets the PHC of the leading interface's NIC. The
leading interface is provided by the `e825` plugin (`leadingInterface`, or its
first configured device) or resolved by HardwareConfig, so HardwareConfig
requires no `e825` plugin configuration. After the initial step, it measures
again and applies one additional correction if the residual offset exceeds one
second in magnitude. A successful `phc_ctl set` exit is completion; the PHC is
not read back.

```yaml
spec:
  profile:
    - name: boundary-clock-tr
      ptp4lConf: |
        [eno1]
        masterOnly 0
        [global]
        domainNumber 24
      plugins:
        phc-first-step:
          timeout: 30s
        e825:
          devices:
            - eno1
```

The `timeout` option is optional and accepts Go duration syntax such as `30s`
or `2m`. It bounds each offset measurement. When omitted, each measurement
waits indefinitely for one valid sample. PHC read and set commands are not
bounded by this timeout.

When the selected profile is invalid, hardware cannot be resolved, measurement
times out, or a command fails, the callback returns an error. The daemon reports
`HardwarePluginReady=False` and continues normal profile application.
