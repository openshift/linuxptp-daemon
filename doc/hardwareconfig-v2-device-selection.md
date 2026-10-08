# HardwareConfig v2 device selection

This document describes how GNSS device detection works and how HardwareConfig v2
selectors identify Ethernet, serial, and USB devices.

## GNSS matcher

`GNSSConfig.Match` currently supports exactly one of:

- `ttyDevice`
- `serialDevice`
- `ethernetDevice`
- `usbDevice`

Hardware-oriented Ethernet and USB lookup must resolve to exactly one usable
GNSS tty; no match or multiple matching tty devices is an error. `ttyDevice` is
returned as supplied. A name-only Ethernet lookup directly reads that
interface's `device/gnss/` directory; if that directory contains multiple GNSS
entries, the current implementation logs a warning and chooses the
lexicographically first entry. This is a legacy exception to strict uniqueness.

### Direct tty selection

When `ttyDevice` is specified, it is returned as provided. This is the most
explicit option, but paths such as `/dev/ttyACM0` can change after reboot or
hotplug events.

### Ethernet-device selection

`ethernetDevice` selects the Ethernet interface associated with the GNSS receiver.
It supports any Linux interface name, a PCI function address, a permanent MAC
address, a firmware-reported PCI slot ID, and PCI vendor/device IDs. For example,
a Westport Channel (E810) setup can select a NIC by its interface name:

```yaml
match:
  ethernetDevice:
    name: ens2f0
```

The name may be any current Linux interface name, including names such as
`eno8703`, `enp2s0`, and `ens2f0`. For hardware-oriented selection, use
`pciAddress`, `permanentMACAddress`, `slot`, `vendorID`, or `deviceID`:

```yaml
match:
  ethernetDevice:
    pciAddress: "0000:86:00.0"
    permanentMACAddress: "00:11:22:aa:bb:cc"
    slot: "2"
    vendorID: "8086"
    deviceID: "159B"
```

The fields mean:

- `pciAddress` is a PCI bus:device.function address (BDF), for example
  `0000:86:00.0`. A short address such as `86:00.0` is normalized to the full
  domain form. It identifies a PCI function, not a chassis slot.
- `permanentMACAddress` is the permanent hardware MAC address, in colon-separated
  form such as `00:11:22:aa:bb:cc`. The daemon reads it using `ethtool -P`;
  it does not use the possibly overridden current MAC address.
- `slot` is the decimal firmware-reported PCI slot ID used in systemd slot-based
  interface names. In `ens2f0`, the slot component is `2`; it is not the PCI
  bus number. Multiple PCI functions in one physical slot can share the slot ID.
- `vendorID` and `deviceID` are hexadecimal PCI IDs. `vendorID: "8086"` and
  `deviceID: "159B"` identify an Intel E810 Westport Channel NIC. Either ID may
  be used alone, or both may be combined; each supplied value is matched against
  the NIC's PCI `vendor` and `device` sysfs attributes.

The Intel E810 behavior profile uses `vendorID: "8086"` and `deviceID: "159B"`
by default for GNSS selection. A single matching card is selected automatically.
If multiple cards match, the error lists each interface and its PCI slot when
available; add the desired `slot` to the selector to disambiguate. User-supplied
Ethernet selector fields are combined with the E810 template defaults, so adding
only `slot` retains the `vendorID` and `deviceID` criteria.

Every supplied selector is combined with the others using AND semantics. A
name-only selector takes the direct `/sys/class/net/<name>/device/gnss/` lookup
path; when a name is combined with hardware selectors, those selectors are
checked against that named interface as well. Vendor/device IDs may identify a
card model shared by multiple cards, so combine them with `slot` when more than
one matching card is present.

For hardware-oriented selector lookup, the daemon enumerates `/sys/class/net`,
resolves each interface's `device` symlink, and checks all requested criteria:
PCI address, permanent MAC, slot, vendor ID, and device ID. PCI IDs are read
from the device's `vendor` and `device` attributes. For `slot`, it first checks
the firmware `_SUN` value exposed through `firmware_node/sun`, then falls back
to PCI slot address mappings under `/sys/bus/pci/slots`. The matching
interface's `device/gnss/` directory is used to resolve the GNSS device node.
A selector with no matching GNSS device returns an error. If multiple GNSS
devices match, the error lists their interfaces and slots when available so a
`slot` can be added to select one.

### ACPI serial-device selection

`serialDevice` identifies an onboard serial controller by platform hardware
identity instead of by the dynamically assigned Linux tty name. The selector
currently supports ACPI identity:

```yaml
serialDevice:
  acpi:
    hid: INTC10EE
```

The detector enumerates `/sys/class/tty/*`, resolves each tty's `device`
symlink, and walks its parent directories looking for an ACPI device directory
named `<hid>:<uid>`. If `uid` is omitted, any instance of the HID may match.
Here `uid` means the Linux ACPI device instance suffix in the sysfs directory
name; it is not the value of the ACPI `_UID` attribute. The result is
`/dev/<tty-name>`, and zero or multiple matching ttys are errors.

For the HPE EL140, HPE documents the GNSS-connected controller as:

```text
ACPI device: INTC10EE:00
Linux driver: dw-apb-uart
Typical tty: /dev/ttyS2
```

The Linux name `ttyS2` is not used as the selector because serial numbering can
change when other UARTs are enumerated. The ACPI HID and UID identify the
controller; the HPE EL140 hardware profile supplies the platform-specific
knowledge that this controller is wired to the GNSS module. The driver name is
not required for matching because it is an implementation detail of the Linux
kernel.

The HPE EL140 profile therefore uses:

```yaml
match:
  serialDevice:
    acpi:
      hid: INTC10EE
```

This is a hardware identity match, not a generic proof that every system with
`INTC10EE:00` has a GNSS receiver attached.

#### Live EL140 verification

The selector was checked on a live EL140 node. Its tty and platform-device
relationships were:

```text
ttyS0 -> /sys/devices/pnp0/00:03
ttyS1 -> /sys/devices/pnp0/00:02
ttyS2 -> /sys/devices/platform/INTC10EE:00
ttyS3 -> /sys/devices/platform/serial8250
```

The matching device reported:

```text
/sys/devices/platform/INTC10EE:00/driver -> /sys/bus/platform/drivers/dw-apb-uart
/sys/devices/platform/INTC10EE:00/modalias = acpi:INTC10EE:
/dev/ttyS2 exists
/dev/ttyACM0 is absent
```

The ACPI sysfs entry also reported `uid = 1` at
`/sys/bus/acpi/devices/INTC10EE:00/uid`. This is distinct from the `:00`
Linux device-instance suffix in the platform-device name. The matcher uses the
platform-device ancestry and therefore deliberately matches `hid: INTC10EE`
without requiring either UID value. It does not need to inspect the UART driver
name; `dw-apb-uart` is recorded as a diagnostic confirmation rather than a
selection criterion.

The live result confirms that the selector resolves `/dev/ttyS2` without relying
on the dynamically assigned tty number. The serial stream itself was not read
during verification.

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
one currently produces a tty.
If multiple identical receivers are connected, the optional `path` narrows the
match to the receiver on that bus-port chain. If the selected receiver itself
exposes multiple matching tty nodes, resolution still fails with an ambiguity
error; the topology path does not select between interfaces of one USB device.
Ambiguity errors and logs list the matched USB topology paths to help identify a
`path` value to add to the HardwareConfig.

## References

- HPE EL140 UART advisory:
  [a00157521en_us](https://support.hpe.com/hpesc/public/docDisplay?docId=a00157521en_us&docLocale=en_US)
- HardwareConfig v2 API: `../ptp-operator/api/v2alpha1/hardwareconfig_types.go`
