# Overriding Board Labels

## Overview

The linuxptp-daemon supports overriding board labels via a Kubernetes ConfigMap. This is useful when your hardware system uses different DPLL pin naming conventions than the default embedded labels (e.g., Dell Perla generations, HPE, Supermicro systems).

Board label remapping applies to:
- **PinDefaults** - Pin configuration defaults
- **Delay Compensation** - Delay compensation model pin references
- **Behavior Profiles** - Pin role assignments in clock behavior profiles

## When to Use This Feature

Use board label remapping when:
- Your hardware system has different pin naming conventions than the embedded defaults
- You need to customize board labels without modifying the entire hardware configuration
- You want to override labels for specific hardware definition paths (e.g., `intel/e825`, `intel/e810`)

**Note:** If no ConfigMap is provided, the daemon uses embedded defaults. This feature is completely optional and backward compatible.

## ConfigMap Setup

### Step 1: Create the ConfigMap

Create a ConfigMap named `board-label-mapping` in the same namespace as the daemon (typically `openshift-ptp`):

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: board-label-mapping
  namespace: openshift-ptp
data:
  # Board label remapping for Intel E825 (GNR-D)
  # Note: ConfigMap keys must be valid (alphanumeric, '-', '_', or '.'), so '/' is replaced with '-'
  "intel-e825": |
    boardLabelMap:
      "GNR-D_SDP0": "PERLA2_SDP0"
      "GNR-D_SDP2": "PERLA2_SDP2"
      "GNR_D_SDP3": "PERLA2_SDP3"
      "OCP1_CLK": "PERLA2_OCP1_CLK"
      "OCP2_CLK": "PERLA2_OCP2_CLK"
      "GNSS_1PPS_IN": "PERLA2_GNSS_IN"
  
  # Board label remapping for Intel E810 (if needed)
  "intel-e810": |
    boardLabelMap:
      "SOME_LABEL": "CUSTOM_LABEL"
```

### Step 2: ConfigMap Structure

**ConfigMap Name:** `board-label-mapping`

**Namespace:** Same as the daemon namespace (typically `openshift-ptp`)

**Data Key Format:** `{hwDefPath}` with `/` replaced by `-`

Since ConfigMap keys must be valid (matching regex `[-._a-zA-Z0-9]+`), forward slashes in the hardware definition path are automatically replaced with hyphens. For example:
- Hardware definition path `intel/e825` → ConfigMap key `intel-e825`
- Hardware definition path `intel/e810` → ConfigMap key `intel-e810`

The daemon automatically converts the hardware definition path to a valid ConfigMap key by replacing `/` with `-`.

**Board Label Map Format:**

Each data key contains a YAML mapping of old labels to new labels:

```yaml
boardLabelMap:
  "OLD_LABEL_1": "NEW_LABEL_1"
  "OLD_LABEL_2": "NEW_LABEL_2"
  # ... more mappings
```

### Step 3: Apply the ConfigMap

```bash
kubectl apply -f board-label-mapping-configmap.yaml
```

The daemon will automatically detect and use the ConfigMap on the next hardware configuration load.

## Examples

### Example 1: Dell Perla4 System

For Dell Perla4 systems that use different labels than standard GNR-D:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: board-label-mapping
  namespace: openshift-ptp
data:
  "intel-e825": |
    boardLabelMap:
      "GNR-D_SDP0": "ETH01_SDP_TIMESYNC_0"
      "GNR-D_SDP2": "ETH01_SDP_TIMESYNC_2"
      "GNR_D_SDP3": "ETH01_SDP_TIMESYNC_3"
      # these two are muxed into one, hence the same label
      "OCP1_CLK": "EXT_10M_OUT_M2_SW"
      "OCP2_CLK": "EXT_10M_OUT_M2_SW"
      # unchanged
      "GNSS_1PPS_IN": "GNSS_1PPS_IN"
```

## How It Works

1. **Label Remapping:** When the daemon loads hardware defaults or behavior profiles, it checks for a board label mapping ConfigMap.
2. **Remapping Application:** If found, all board labels are remapped according to the mapping before being used.
3. **Caching:** Remapped configurations are cached for performance, so remapping only occurs once per hardware definition path.
4. **Fallback:** If the ConfigMap is missing or invalid, the daemon falls back to embedded defaults and logs a warning.

## Remapping Rules

- **Exact Match:** Remapping applies only to exact string matches (case-sensitive)
- **No Partial Matching:** Substrings are not remapped
- **One-Way Mapping:** Maps old labels → new labels
- **Multiple Hardware Paths:** You can define mappings for multiple hardware definition paths in the same ConfigMap

## Troubleshooting

### ConfigMap Not Found

If the ConfigMap is not found, the daemon will:
- Log a warning message
- Fall back to embedded defaults
- Continue operating normally

**Check:**
- ConfigMap name is `board-label-mapping`
- ConfigMap is in the correct namespace (same as daemon)
- ConfigMap exists: `kubectl get configmap board-label-mapping -n openshift-ptp`

### Invalid YAML Format

If the ConfigMap contains invalid YAML:
- The daemon will log an error
- Fall back to embedded defaults
- Continue operating normally

**Check:**
- YAML syntax is correct
- The `boardLabelMap` key exists
- All labels are quoted strings

### Labels Not Remapping

If labels are not being remapped:

1. **Verify the hardware definition path:**
   - Check what `hwDefPath` your system uses (check daemon logs)
   - Convert the hardware definition path to a ConfigMap key by replacing `/` with `-` (e.g., `intel/e825` → `intel-e825`)
   - Ensure the ConfigMap key matches this converted format

2. **Verify label names:**
   - Ensure old label names match exactly (case-sensitive)
   - Check daemon logs for remapping operations

3. **Check daemon logs:**
   ```bash
   kubectl logs -n openshift-ptp -l app=linuxptp-daemon | grep -i "board label"
   ```

### Finding Current Board Labels

To find the current board labels used by your system:

1. Check the embedded defaults in the daemon source code
2. Check hardware configuration logs
3. Inspect the `HardwareConfig` resource status

## Best Practices

1. **Test First:** Test board label remapping in a non-production environment first
2. **Minimal Changes:** Only remap labels that differ from defaults
3. **Documentation:** Document your custom label mappings for your team
4. **Version Control:** Store ConfigMap definitions in version control
5. **Validation:** Validate that remapped labels match your actual hardware pin names

## Limitations

- Board label remapping only affects labels, not other vendor defaults
- Remapping is applied at load time and cached
- Changes to the ConfigMap require daemon restart or hardware configuration reload to take effect
- Labels must match exactly (case-sensitive, no partial matching)
