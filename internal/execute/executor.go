package execute

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ciwg-cli/internal/auth"
)

// ScriptExecution represents a script execution context
type ScriptExecution struct {
	ID        string
	Script    string
	Args      []string
	Status    ExecutionStatus
	StartTime time.Time
	EndTime   *time.Time
	Output    string
	Error     string
	ExitCode  int
}

// ExecutionStatus represents the status of a script execution
type ExecutionStatus string

const (
	StatusPending   ExecutionStatus = "pending"
	StatusRunning   ExecutionStatus = "running"
	StatusCompleted ExecutionStatus = "completed"
	StatusFailed    ExecutionStatus = "failed"
	StatusCancelled ExecutionStatus = "cancelled"
)

// ScriptQueue represents a FIFO queue for script execution
type ScriptQueue struct {
	executions []ScriptExecution
	mutex      sync.RWMutex
	maxSize    int
}

// ScriptChain represents a chain of scripts where output feeds into the next
type ScriptChain struct {
	Scripts []ChainedScript
	Context context.Context
}

// ChainedScript represents a script in a chain
type ChainedScript struct {
	Script string
	Args   []string
	Filter string // Optional filter for processing output
}

// Executor handles script execution operations
type Executor struct {
	sshClient     *auth.SSHClient
	queue         *ScriptQueue
	localScripts  map[string]string
	remoteScripts map[string]string
}

// NewExecutor creates a new script executor
func NewExecutor(sshClient *auth.SSHClient) *Executor {
	return &Executor{
		sshClient:     sshClient,
		queue:         NewScriptQueue(100),
		localScripts:  make(map[string]string),
		remoteScripts: make(map[string]string),
	}
}

// NewScriptQueue creates a new script queue with the specified maximum size
func NewScriptQueue(maxSize int) *ScriptQueue {
	return &ScriptQueue{
		executions: make([]ScriptExecution, 0),
		maxSize:    maxSize,
	}
}

// LoadLocalScripts loads scripts from local directories
func (e *Executor) LoadLocalScripts(scriptDirs []string) error {
	for _, dir := range scriptDirs {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			if !info.IsDir() && strings.HasSuffix(path, ".sh") {
				// Use relative path from script directory as script name
				relativePath, err := filepath.Rel(dir, path)
				if err != nil {
					return err
				}

				// Remove .sh extension for script name
				scriptName := strings.TrimSuffix(relativePath, ".sh")
				e.localScripts[scriptName] = path
			}

			return nil
		})

		if err != nil {
			return fmt.Errorf("failed to load scripts from %s: %w", dir, err)
		}
	}

	return nil
}

// ListAvailableScripts lists all available scripts (local and remote)
func (e *Executor) ListAvailableScripts() map[string]string {
	scripts := make(map[string]string)

	// Add local scripts
	for name, path := range e.localScripts {
		scripts[name] = fmt.Sprintf("local:%s", path)
	}

	// Add remote scripts (if any are discovered)
	for name, path := range e.remoteScripts {
		scripts[name] = fmt.Sprintf("remote:%s", path)
	}

	return scripts
}

// ExecuteScript executes a single script
func (e *Executor) ExecuteScript(scriptName string, args []string, interactive bool) (*ScriptExecution, error) {
	execution := ScriptExecution{
		ID:        generateExecutionID(),
		Script:    scriptName,
		Args:      args,
		Status:    StatusPending,
		StartTime: time.Now(),
	}

	// Check if script exists
	scriptPath, exists := e.localScripts[scriptName]
	if !exists {
		execution.Status = StatusFailed
		execution.Error = fmt.Sprintf("script %s not found", scriptName)
		return &execution, fmt.Errorf("script %s not found", scriptName)
	}

	execution.Status = StatusRunning

	// Copy script to remote server
	remoteScriptPath := fmt.Sprintf("/tmp/%s_%s.sh", scriptName, execution.ID)
	if err := e.sshClient.CopyFile(scriptPath, remoteScriptPath); err != nil {
		execution.Status = StatusFailed
		execution.Error = fmt.Sprintf("failed to copy script: %v", err)
		return &execution, err
	}

	// Make script executable
	_, _, err := e.sshClient.ExecuteCommand(fmt.Sprintf("chmod +x %s", remoteScriptPath))
	if err != nil {
		execution.Status = StatusFailed
		execution.Error = fmt.Sprintf("failed to make script executable: %v", err)
		return &execution, err
	}

	// Build command with arguments
	command := remoteScriptPath
	if len(args) > 0 {
		command += " " + strings.Join(args, " ")
	}

	// Execute script
	if interactive {
		err = e.sshClient.ExecuteInteractiveCommand(command, os.Stdout, os.Stderr)
	} else {
		var stdout, stderr string
		stdout, stderr, err = e.sshClient.ExecuteCommand(command)
		execution.Output = stdout
		if stderr != "" {
			execution.Error = stderr
		}
	}

	// Clean up remote script
	e.sshClient.ExecuteCommand(fmt.Sprintf("rm -f %s", remoteScriptPath))

	// Update execution status
	endTime := time.Now()
	execution.EndTime = &endTime

	if err != nil {
		execution.Status = StatusFailed
		if execution.Error == "" {
			execution.Error = err.Error()
		}
		execution.ExitCode = 1
	} else {
		execution.Status = StatusCompleted
		execution.ExitCode = 0
	}

	return &execution, err
}

// QueueScript adds a script to the execution queue
func (e *Executor) QueueScript(scriptName string, args []string) (string, error) {
	execution := ScriptExecution{
		ID:        generateExecutionID(),
		Script:    scriptName,
		Args:      args,
		Status:    StatusPending,
		StartTime: time.Now(),
	}

	if err := e.queue.Add(execution); err != nil {
		return "", err
	}

	return execution.ID, nil
}

// ProcessQueue processes all scripts in the queue
func (e *Executor) ProcessQueue() error {
	for {
		execution := e.queue.Next()
		if execution == nil {
			break
		}

		result, err := e.ExecuteScript(execution.Script, execution.Args, false)
		e.queue.UpdateStatus(execution.ID, result.Status)

		if err != nil {
			fmt.Printf("Script %s failed: %v\n", execution.Script, err)
		} else {
			fmt.Printf("Script %s completed successfully\n", execution.Script)
		}
	}

	return nil
}

// ExecuteChain executes a chain of scripts where output feeds into the next
func (e *Executor) ExecuteChain(chain ScriptChain) error {
	var previousOutput string

	for i, chainedScript := range chain.Scripts {
		fmt.Printf("Executing chain step %d: %s\n", i+1, chainedScript.Script)

		// Prepare arguments
		args := chainedScript.Args
		if i > 0 && previousOutput != "" {
			// Add previous output as the last argument
			args = append(args, previousOutput)
		}

		// Execute script
		execution, err := e.ExecuteScript(chainedScript.Script, args, false)
		if err != nil {
			return fmt.Errorf("chain failed at step %d (%s): %w", i+1, chainedScript.Script, err)
		}

		// Process output for next step
		output := execution.Output
		if chainedScript.Filter != "" {
			output = e.applyFilter(output, chainedScript.Filter)
		}

		previousOutput = strings.TrimSpace(output)
		fmt.Printf("Step %d output: %s\n", i+1, previousOutput)
	}

	return nil
}

// applyFilter applies a simple filter to the output
func (e *Executor) applyFilter(output, filter string) string {
	lines := strings.Split(output, "\n")
	var filteredLines []string

	switch filter {
	case "first":
		if len(lines) > 0 {
			return lines[0]
		}
	case "last":
		if len(lines) > 0 {
			return lines[len(lines)-1]
		}
	case "trim":
		return strings.TrimSpace(output)
	default:
		// Try to use the filter as a grep-like pattern
		for _, line := range lines {
			if strings.Contains(line, filter) {
				filteredLines = append(filteredLines, line)
			}
		}
		return strings.Join(filteredLines, "\n")
	}

	return output
}

// Add adds an execution to the queue
func (q *ScriptQueue) Add(execution ScriptExecution) error {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	if len(q.executions) >= q.maxSize {
		return fmt.Errorf("queue is full (max %d)", q.maxSize)
	}

	q.executions = append(q.executions, execution)
	return nil
}

// Next returns the next execution in the queue (FIFO)
func (q *ScriptQueue) Next() *ScriptExecution {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	if len(q.executions) == 0 {
		return nil
	}

	execution := q.executions[0]
	q.executions = q.executions[1:]
	return &execution
}

// List returns all executions in the queue
func (q *ScriptQueue) List() []ScriptExecution {
	q.mutex.RLock()
	defer q.mutex.RUnlock()

	executions := make([]ScriptExecution, len(q.executions))
	copy(executions, q.executions)
	return executions
}

// UpdateStatus updates the status of an execution in the queue
func (q *ScriptQueue) UpdateStatus(id string, status ExecutionStatus) {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	for i := range q.executions {
		if q.executions[i].ID == id {
			q.executions[i].Status = status
			if status == StatusCompleted || status == StatusFailed || status == StatusCancelled {
				endTime := time.Now()
				q.executions[i].EndTime = &endTime
			}
			break
		}
	}
}

// Size returns the current size of the queue
func (q *ScriptQueue) Size() int {
	q.mutex.RLock()
	defer q.mutex.RUnlock()
	return len(q.executions)
}

// Clear removes all executions from the queue
func (q *ScriptQueue) Clear() {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	q.executions = q.executions[:0]
}

// generateExecutionID generates a unique execution ID
func generateExecutionID() string {
	return fmt.Sprintf("exec_%d", time.Now().UnixNano())
}

// InteractiveScriptSelector allows user to select and execute scripts interactively
func (e *Executor) InteractiveScriptSelector() error {
	scripts := e.ListAvailableScripts()
	if len(scripts) == 0 {
		return fmt.Errorf("no scripts available")
	}

	fmt.Println("=== Available Scripts ===")
	scriptNames := make([]string, 0, len(scripts))
	for name := range scripts {
		scriptNames = append(scriptNames, name)
	}

	for i, name := range scriptNames {
		fmt.Printf("%d. %s (%s)\n", i+1, name, scripts[name])
	}

	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Select script number: ")
	input, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read input: %w", err)
	}

	var selection int
	if _, err := fmt.Sscanf(strings.TrimSpace(input), "%d", &selection); err != nil {
		return fmt.Errorf("invalid selection: %w", err)
	}

	if selection < 1 || selection > len(scriptNames) {
		return fmt.Errorf("invalid selection: %d", selection)
	}

	selectedScript := scriptNames[selection-1]

	// Get arguments
	fmt.Print("Enter arguments (optional): ")
	argsInput, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read arguments: %w", err)
	}

	args := []string{}
	if argsStr := strings.TrimSpace(argsInput); argsStr != "" {
		args = strings.Fields(argsStr)
	}

	// Execute script interactively
	fmt.Printf("Executing %s with args: %v\n", selectedScript, args)
	_, err = e.ExecuteScript(selectedScript, args, true)
	return err
}
