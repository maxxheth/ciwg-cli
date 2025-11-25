package cmd

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	backupcmd "ciwg-cli/internal/cmd/backup"
	dnsbackupcmd "ciwg-cli/internal/cmd/dnsbackup"
)

var (
	cfgFile string
	rootCmd = &cobra.Command{
		Use:   "ciwg",
		Short: "CIWG CLI - A general-purpose CLI for managing WordPress infrastructure",
		Long: `CIWG CLI is a comprehensive command-line tool for managing WordPress infrastructure.
It provides functionality for SSH authentication, cron job management, and remote script execution.`,
		Version: "1.0.0",
	}
)

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.ciwg.yaml)")
	rootCmd.PersistentFlags().Bool("verbose", false, "verbose output")
	viper.BindPFlag("verbose", rootCmd.PersistentFlags().Lookup("verbose"))

	// Add backup command from the backup subpackage
	rootCmd.AddCommand(backupcmd.BackupCmd)
	rootCmd.AddCommand(dnsbackupcmd.Cmd)

	// Load environment variables from a .env file in the current directory.
	// If the .env file doesn't exist, that's fine - environment variables can still be set in the shell.
	// Only warn on actual errors (permissions, parse errors, etc.)
	if err := godotenv.Load(); err != nil {
		// Check if it's just a "file not found" error
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Warning: Error loading .env file: %v\n", err)
		}
	}
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		home, err := os.UserHomeDir()
		cobra.CheckErr(err)

		viper.AddConfigPath(home)
		viper.SetConfigType("yaml")
		viper.SetConfigName(".ciwg")
	}

	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err == nil {
		if viper.GetBool("verbose") {
			fmt.Fprintln(os.Stderr, "Using config file:", viper.ConfigFileUsed())
		}
	}
}
