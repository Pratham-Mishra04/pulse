package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print Pulse version and Go runtime info",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("pulse %s\n", Version)
		fmt.Printf("go    %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	},
}
