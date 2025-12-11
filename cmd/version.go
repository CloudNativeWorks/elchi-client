package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Version command
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show version information",
	Long:  `Display detailed version information about the client.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("Version: %s\n", Version)
	},
}

func init() {
	RootCmd.AddCommand(versionCmd)
}
