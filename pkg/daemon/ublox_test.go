package daemon_test

import (
	"bufio"
	"fmt"
	"github.com/openshift/linuxptp-daemon/pkg/daemon"
	"os/exec"
	"testing"
	"time"
)

func TestUBX_CmdRun(t *testing.T) {
	u := daemon.UBX{}
	u.CmdInit()
	u.CmdRun(false)

}

func test_test(t *testing.T) {
	command := "ubxtool"
	args := []string{"-o", "NAV-STATUS", "-w", "1000000000"}

	for {
		cmd := exec.Command(command, args...)

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			fmt.Println("Error creating StdoutPipe:", err)
			return
		}

		if err := cmd.Start(); err != nil {
			fmt.Println("Error starting command:", err)
			return
		}

		scanner := bufio.NewScanner(stdout)

		for scanner.Scan() {
			// Read and process each line of output here
			line := scanner.Text()
			fmt.Println("Output:", line)
		}

		if err := cmd.Wait(); err != nil {
			fmt.Println("Error waiting for command:", err)
		}

		// Add a delay before retrying
		time.Sleep(5 * time.Second)
	}
}
