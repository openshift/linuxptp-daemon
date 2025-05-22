# Feature Flags

This package provides a central point of truth for the codebase, indicating which features are available for execution.
It determines feature availability based on the installed version of the linuxptp package.
Integrating this package allows for conditional code execution depending on the enabled features.

## Example

```go
import (
    "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/features"
)

func Foo() {
    if features.Flags.GM.Enabled {
        fmt.Println("Generic GM stuff here")
        if features.Flags.GM.Holdover {
            fmt.Println("Holdover GM stuff here")
        }
    }
}
```
