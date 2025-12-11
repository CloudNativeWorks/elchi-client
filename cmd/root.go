package cmd

import (
	"fmt"
	"os"

	"github.com/CloudNativeWorks/elchi-client/internal/config"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	cfgFile string
	clientName string
	Cfg     *config.Config
	Version string
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
	var err error

	// Load configuration
	if cfgFile != "" {
		Cfg, err = config.LoadConfig(cfgFile)
		if err != nil {
			fmt.Printf("Fatal: Configuration could not be loaded: %v\n", err)
			os.Exit(1)
		}
	} else {
		// Load default config
		Cfg = config.DefaultConfig()

		// Initialize logger with default config
		if err := logger.Init(logger.Config{
			Level:      Cfg.Logging.Level,
			Format:     Cfg.Logging.Format,
			Module:     "root",
		}); err != nil {
			fmt.Printf("Fatal: Logger could not be initialized: %v\n", err)
			os.Exit(1)
		}

		// Configure Viper for future use
		viper.SetConfigName("config")
		viper.SetConfigType("yaml")
		viper.AddConfigPath(".")

		// Try to read config file if exists
		if err := viper.ReadInConfig(); err != nil {
			if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
				// Only exit if error is not "config file not found"
				fmt.Printf("Fatal: Configuration file could not be read: %v\n", err)
				os.Exit(1)
			}
		}
	}

	// Override client name if provided via command line flag
	if clientName != "" {
		Cfg.Client.Name = clientName
	}
}
