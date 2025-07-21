package cmd

import (
	"fmt"
	"os"
	"time"

	"ciwg-cli/internal/auth"
	"ciwg-cli/internal/utils"

	"github.com/spf13/cobra"
)

var (
	zipPath string
	domain  string
	url     string
	dbName  string
)

var migrateCmd = &cobra.Command{
	Use:   "migrate [user@]host",
	Short: "Migrate a WordPress site from a zip file, or create a new one",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		// pull the cobra flag and shove it into the std-flag pointer
		skelDir, _ := cmd.Flags().GetString("skel-dir")
		if skelDir != "" {
			*utils.SkelDirFlag = skelDir
		}

		dryRun, _ := cmd.Flags().GetBool("dry-run")
		local, _ := cmd.Flags().GetBool("local")
		createNewSite, _ := cmd.Flags().GetBool("create-new-site")

		if domain == "" && !dryRun {
			fmt.Fprintln(os.Stderr, "Error: domain is required when not in dry-run mode.")
			os.Exit(1)
		}

		var wpPath, sqlPath string
		var err error

		if zipPath != "" {
			wpPath, sqlPath, err = utils.InspectZipFile(zipPath)
			if err != nil {
				if createNewSite {
					fmt.Println("Warning: Could not find a valid WordPress installation or SQL file in the provided zip. Proceeding with new site creation as --create-new-site is set.")
					// By setting paths to empty, we signal to the next functions to create a new site.
					wpPath = ""
					sqlPath = ""
				} else {
					fmt.Fprintf(os.Stderr, "Error inspecting zip file: %v\n", err)
					fmt.Fprintln(os.Stderr, "Hint: If you intended to create a new blank site from this zip's contents, use the --create-new-site flag.")
					os.Exit(1)
				}
			}
		} else {
			if !createNewSite {
				fmt.Fprintln(os.Stderr, "Error: --zip flag is required unless --create-new-site is specified.")
				os.Exit(1)
			}
			// No zip provided, so definitely a new site.
			wpPath = ""
			sqlPath = ""
		}

		projectPath := "./" + domain

		if local {
			if err := utils.SpinUpSiteLocal(domain, url, dbName, zipPath, wpPath, sqlPath, projectPath, createNewSite, dryRun); err != nil {
				fmt.Fprintf(os.Stderr, "Error spinning up site locally: %v\n", err)
				os.Exit(1)
			}
		} else {
			if len(args) == 0 {
				fmt.Fprintln(os.Stderr, "Error: remote host argument is required when not running in local mode.")
				os.Exit(1)
			}
			host := args[0]
			user, _ := cmd.Flags().GetString("user")
			port, _ := cmd.Flags().GetString("port")
			key, _ := cmd.Flags().GetString("key")
			agent, _ := cmd.Flags().GetBool("agent")
			timeout, _ := cmd.Flags().GetDuration("timeout")

			sshConfig := auth.SSHConfig{
				Hostname: host,
				Username: user,
				Port:     port,
				KeyPath:  key,
				UseAgent: agent,
				Timeout:  timeout,
			}

			sshClient, err := auth.NewSSHClient(sshConfig)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error creating SSH client: %v\n", err)
				os.Exit(1)
			}
			defer sshClient.Close()

			if err := utils.SpinUpSite(sshClient, domain, url, dbName, zipPath, wpPath, sqlPath, projectPath, createNewSite, dryRun); err != nil {
				fmt.Fprintf(os.Stderr, "Error spinning up site remotely: %v\n", err)
				os.Exit(1)
			}
		}

		if dryRun {
			fmt.Println("\nDry run complete! No changes were made.")
		} else {
			fmt.Println("Migration complete!")
		}
	},
}

func init() {
	rootCmd.AddCommand(migrateCmd)
	migrateCmd.Flags().StringVarP(&zipPath, "zip", "z", "", "Path to the zip file (optional if creating a new site)")
	migrateCmd.Flags().StringVarP(&domain, "domain", "d", "", "Domain for the new site")
	migrateCmd.Flags().StringVarP(&url, "url", "u", "", "URL for the new site")
	migrateCmd.Flags().StringVarP(&dbName, "dbname", "n", "", "Database name")
	migrateCmd.Flags().Bool("dry-run", false, "Perform a dry run without making any changes")
	migrateCmd.Flags().Bool("local", false, "Run locally without SSH")
	migrateCmd.Flags().Bool("create-new-site", false, "If zip is invalid or not provided, create a new blank WordPress site")

	migrateCmd.Flags().String("user", "", "SSH username (default: current user)")
	migrateCmd.Flags().StringP("port", "p", "22", "SSH port")
	migrateCmd.Flags().StringP("key", "k", "", "Path to SSH private key")
	migrateCmd.Flags().BoolP("agent", "a", true, "Use SSH agent")
	migrateCmd.Flags().DurationP("timeout", "t", 30*time.Second, "Connection timeout")
}
