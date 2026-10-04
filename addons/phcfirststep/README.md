# PHC First-Step Plugin

`phc-first-step` synchronously applies the initial PHC correction for a
time-receiver profile before normal daemon profile application continues.
The operator enables the plugin by default, but a profile runs it only when
its `plugins` map contains `phc-first-step`.

The plugin identifies each TR interface from a `ptp4lConf` interface section
with `masterOnly 0`. All selected interfaces must resolve to one PHC, and at
least one device in the same profile's `e825.devices` must expose that PHC. Its
name may differ from the TR interface name. The plugin runs free-running
`ptp4l` until it receives one valid master-offset sample with non-zero path
delay. It then reads the shared PHC and sets it to the current PHC time minus
that offset. After the initial step, it measures again and applies one
additional correction if the residual offset exceeds one second in magnitude.
A successful `phc_ctl set` exit is completion; the PHC is not read back.

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
