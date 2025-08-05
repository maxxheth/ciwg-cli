package cmd

import (
	"fmt"
	l "log"
	"strings"
	"time"

	"ciwg-cli/internal/auth"
	"ciwg-cli/internal/execute"

	"github.com/spf13/cobra"
)

var (
	scriptDirs  []string
	interactive bool
	localExec   bool
	// SSH flags
	sshHost     string
	sshUser     string
	sshPort     string
	sshKey      string
	sshUseAgent bool
	sshTimeout  time.Duration
)

func executeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "execute [script-name] [args...]",
		Short: "Executes a script locally or on a remote server.",
		Long: `Executes a script from a local script directory.

By default, the script is executed on a remote server via SSH.
The command will look for the specified script, copy it to the remote server,
make it executable, run it, and then clean up.

Use the --local flag to execute the script on the local machine instead.

If no script name is provided, an interactive selector will be shown to choose
from the available scripts for remote execution.`,
		RunE: runExecute,
	}

	// Execution flags
	cmd.Flags().StringSliceVar(&scriptDirs, "script-dir", []string{"./scripts"}, "Directories to search for scripts.")
	cmd.Flags().BoolVarP(&interactive, "interactive", "i", false, "Run the script in interactive mode.")
	cmd.Flags().BoolVar(&localExec, "local", false, "Execute the script locally instead of remotely.")

	// SSH Flags
	cmd.Flags().StringVar(&sshHost, "ssh-host", "", "SSH host for remote execution")
	cmd.Flags().StringVar(&sshUser, "ssh-user", "", "SSH user for remote execution (can be embedded in host as user@host)")
	cmd.Flags().StringVar(&sshPort, "ssh-port", "22", "SSH port for remote execution")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "Path to SSH private key. Defaults to standard locations if empty.")
	cmd.Flags().BoolVar(&sshUseAgent, "ssh-agent", true, "Use SSH agent for authentication.")
	cmd.Flags().DurationVar(&sshTimeout, "ssh-timeout", 30*time.Second, "SSH connection timeout.")

	return cmd
}

func init() {
	// Assumes 'rootCmd' is a global variable in the cmd package
	rootCmd.AddCommand(executeCmd())
}

func runExecute(cmd *cobra.Command, args []string) error {
	var executor *execute.Executor

	if localExec {
		// For local execution, we don't need an SSH client.
		executor = execute.NewExecutor(nil)
	} else {
		// Remote execution requires SSH details
		if sshHost == "" {
			return fmt.Errorf("must provide --ssh-host for remote execution")
		}

		host := sshHost
		user := sshUser

		// Allow user@host format in ssh-host flag
		if user == "" && strings.Contains(host, "@") {
			parts := strings.SplitN(host, "@", 2)
			user = parts[0]
			host = parts[1]
		}

		if user == "" {
			return fmt.Errorf("SSH user must be provided via --ssh-user or in --ssh-host (user@host)")
		}

		// Construct SSHConfig
		config := auth.SSHConfig{
			Hostname: host,
			Username: user,
			Port:     sshPort,
			KeyPath:  sshKey,
			UseAgent: sshUseAgent,
			Timeout:  sshTimeout,
		}

		sshClient, err := auth.NewSSHClient(config)
		if err != nil {
			return fmt.Errorf("failed to create SSH client: %w", err)
		}
		executor = execute.NewExecutor(sshClient)
	}

	if err := executor.LoadLocalScripts(scriptDirs); err != nil {
		return fmt.Errorf("failed to load scripts: %w", err)
	}

	if len(args) == 0 {
		if localExec {
			return fmt.Errorf("must provide a script name for local execution")
		}
		l.Println("No script name provided. Launching interactive selector for remote execution...")
		return executor.InteractiveScriptSelector()
	}

	scriptName := args[0]
	scriptArgs := args[1:]

	var execution *execute.ScriptExecution
	var err error

	if localExec {
		l.Printf("Executing script '%s' locally with args: %v", scriptName, scriptArgs)
		execution, err = executor.ExecuteLocalScript(scriptName, scriptArgs, interactive)
	} else {
		l.Printf("Executing script '%s' remotely with args: %v", scriptName, scriptArgs)
		execution, err = executor.ExecuteScript(scriptName, scriptArgs, interactive)
	}

	if err != nil {
		if execution != nil && execution.Error != "" {
			l.Printf("Script execution failed. Stderr:\n%s", execution.Error)
		}
		return fmt.Errorf("error during script execution: %w", err)
	}

	l.Printf("Script '%s' completed successfully.", execution.Script)
	if !interactive {
		if execution.Output != "" {
			fmt.Println("\n--- STDOUT ---")
			fmt.Println(strings.TrimSpace(execution.Output))
			fmt.Println("--------------")
		}
		if execution.Error != "" {
			fmt.Println("\n--- STDERR ---")
			fmt.Println(strings.TrimSpace(execution.Error))
			fmt.Println("--------------")
		}
	}

	return nil
}
