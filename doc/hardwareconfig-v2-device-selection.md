# HardwareConfig v2 GNSS device selection

This document describes how GNSS device detection works and how HardwareConfig v2
selectors identify USB devices and the legacy Ethernet interface field.

## GNSS matcher

`GNSSConfig.Match` supports one of:

- `ttyDevice`
- `ethernetInterface` (legacy)
- `usbDevice`

`ttyDevice` is returned as supplied. The legacy `ethernetInterface` selector
looks up the GNSS device beneath the named network interface. USB lookup must
resolve to one usable GNSS tty; no match or multiple matching tty devices is an
error.

### Direct tty selection

When `ttyDevice` is specified, it is returned as provided. This is the most
explicit option, but paths such as `/dev/ttyACM0` can change after reboot or
hotplug events.

### Legacy Ethernet-interface selection

`ethernetInterface` selects a GNSS device through a Linux network interface
name. The daemon reads `/sys/class/net/<name>/device/gnss/`. If that directory
contains multiple GNSS entries, the current implementation logs a warning and
chooses the lexicographically first entry. This is a legacy name-based selector.

### USB-device selection

The GNR-D GNSS receiver is selected by its USB vendor and product IDs:

```yaml
match:
  usbDevice:
    vendor: "1546"
    product: "01a9"
```

If multiple identical receivers may be connected, specify the optional USB
bus-port topology `path` as an additional selector:

```yaml
match:
  usbDevice:
    vendor: "1546"
    product: "01a9"
    path: "2-1.4"
```

The path is the bus-and-port chain shown in sysfs, such as the `2-1.4` device
node in `/sys/devices/.../usb2/2-1/2-1.4`. It identifies where the receiver is
connected, not the receiver itself, so it changes if the device is moved or the
USB topology changes. Vendor, product, and path are all required to match when
`path` is supplied.

The detector does not depend on the tty name or on the `ttyACM`/`ttyUSB` driver
name. It performs the following sysfs traversal:

1. Enumerate `/sys/class/tty/*`.
2. Ignore entries without a resolvable `device` symlink. This excludes virtual
   tty devices and stale class entries.
3. Resolve `/sys/class/tty/<tty>/device`.
4. Walk the resolved device's parent directories.
5. Identify USB device ancestors by their `subsystem` symlink and read
   `idVendor` and `idProduct`.
6. Compare the normalized hexadecimal IDs with the requested selector and, when
   `path` is specified, require the USB device node's bus-port path to match.
7. Return `/dev/<tty-name>` if exactly one tty matches.

On the GNR-D system the relevant topology is:

```text
/sys/class/tty/ttyACM0/device
  -> /sys/devices/.../usb1/1-4/1-4:1.0
  -> /sys/devices/.../usb1/1-4
```

The USB attributes are located at the `1-4` device node:

```text
/sys/devices/.../usb1/1-4/idVendor      = 1546
/sys/devices/.../usb1/1-4/idProduct     = 01a9
/sys/devices/.../usb1/1-4/manufacturer  = u-blox AG - www.u-blox.com
/sys/devices/.../usb1/1-4/product       = u-blox GNSS receiver
```

The result is `/dev/ttyACM0`. The device has multiple USB interfaces, but only
one currently produces a tty. If multiple identical receivers are connected,
the optional `path` narrows the match to the receiver on that bus-port chain. If
the selected receiver itself exposes multiple matching tty nodes, resolution
still fails with an ambiguity error; the topology path does not select between
interfaces of one USB device. Ambiguity errors and logs list the matched USB
topology paths to help identify a `path` value to add to the HardwareConfig.
