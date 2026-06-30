package cmd

import (
	"fmt"
	"os"

	"github.com/CloudNativeWorks/elchi-client/internal/config"
	"github.com/spf13/cobra"
)

var (
	cfgFile    string
	clientName string
	Cfg        *config.Config
	Version    string
)

var RootCmd = &cobra.Command{
	Use:   "elchi-client",
	Short: "Elchi Client - A gRPC client that communicates with a remote server",
	Long:  `Elchi Client, a gRPC client that securely communicates with a remote server and processes commands.`,
}

func Execute(version string) error {
	Version = version
	return RootCmd.Execute()
}

func init() {
	cobra.OnInitialize(initConfig)
	RootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: ./config.yaml)")
	RootCmd.PersistentFlags().StringVarP(&clientName, "name", "n", "", "client name (overrides config file)")
}

func initConfig() {
	// LoadConfig handles both an explicit --config path and default-path
	// discovery (cwd, $HOME/.elchi, /etc/elchi) when cfgFile is empty. It reads
	// env vars, unmarshals the file into the struct, applies defaults for
	// anything missing, tolerates a missing file, and initializes the logger.
	//
	// The previous empty-path branch only called viper.ReadInConfig() WITHOUT
	// Unmarshal, so a present config.yaml was silently ignored when --config was
	// not passed and the client fell back to hard-coded defaults (wrong host /
	// empty token). Funnelling both cases through LoadConfig fixes that.
	var err error
	Cfg, err = config.LoadConfig(cfgFile)
	if err != nil {
		fmt.Printf("Fatal: Configuration could not be loaded: %v\n", err)
		os.Exit(1)
	}

	// Override client name if provided via command line flag
	if clientName != "" {
		Cfg.Client.Name = clientName
	}
}
