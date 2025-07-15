package execute

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestScriptQueue_Operations(t *testing.T) {
	queue := NewScriptQueue(3)

	// Test empty queue
	if queue.Size() != 0 {
		t.Errorf("Expected empty queue size 0, got %d", queue.Size())
	}

	if queue.Next() != nil {
		t.Errorf("Expected nil from empty queue")
	}

	// Test adding executions
	exec1 := ScriptExecution{
		ID:     "test1",
		Script: "script1.sh",
		Status: StatusPending,
	}

	exec2 := ScriptExecution{
		ID:     "test2",
		Script: "script2.sh",
		Status: StatusPending,
	}

	exec3 := ScriptExecution{
		ID:     "test3",
		Script: "script3.sh",
		Status: StatusPending,
	}

	if err := queue.Add(exec1); err != nil {
		t.Errorf("Failed to add first execution: %v", err)
	}

	if err := queue.Add(exec2); err != nil {
		t.Errorf("Failed to add second execution: %v", err)
	}

	if err := queue.Add(exec3); err != nil {
		t.Errorf("Failed to add third execution: %v", err)
	}

	if queue.Size() != 3 {
		t.Errorf("Expected queue size 3, got %d", queue.Size())
	}

	// Test queue full
	exec4 := ScriptExecution{
		ID:     "test4",
		Script: "script4.sh",
		Status: StatusPending,
	}

	if err := queue.Add(exec4); err == nil {
		t.Errorf("Expected error when adding to full queue")
	}

	// Test FIFO order
	next := queue.Next()
	if next == nil || next.ID != "test1" {
		t.Errorf("Expected first execution to be test1, got %v", next)
	}

	if queue.Size() != 2 {
		t.Errorf("Expected queue size 2 after next, got %d", queue.Size())
	}

	// Test status update
	queue.UpdateStatus("test2", StatusRunning)
	executions := queue.List()
	found := false
	for _, exec := range executions {
		if exec.ID == "test2" && exec.Status == StatusRunning {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Status update failed")
	}

	// Test clear
	queue.Clear()
	if queue.Size() != 0 {
		t.Errorf("Expected empty queue after clear, got size %d", queue.Size())
	}
}

func TestExecutionStatus(t *testing.T) {
	statuses := []ExecutionStatus{
		StatusPending,
		StatusRunning,
		StatusCompleted,
		StatusFailed,
		StatusCancelled,
	}

	expectedValues := []string{
		"pending",
		"running",
		"completed",
		"failed",
		"cancelled",
	}

	for i, status := range statuses {
		if string(status) != expectedValues[i] {
			t.Errorf("Expected status %s, got %s", expectedValues[i], string(status))
		}
	}
}

func TestScriptExecution_Structure(t *testing.T) {
	startTime := time.Now()
	endTime := startTime.Add(time.Minute)

	execution := ScriptExecution{
		ID:        "test123",
		Script:    "test-script.sh",
		Args:      []string{"arg1", "arg2"},
		Status:    StatusCompleted,
		StartTime: startTime,
		EndTime:   &endTime,
		Output:    "test output",
		Error:     "",
		ExitCode:  0,
	}

	if execution.ID != "test123" {
		t.Errorf("ID not set correctly")
	}
	if execution.Script != "test-script.sh" {
		t.Errorf("Script not set correctly")
	}
	if len(execution.Args) != 2 || execution.Args[0] != "arg1" || execution.Args[1] != "arg2" {
		t.Errorf("Args not set correctly")
	}
	if execution.Status != StatusCompleted {
		t.Errorf("Status not set correctly")
	}
	if !execution.StartTime.Equal(startTime) {
		t.Errorf("StartTime not set correctly")
	}
	if execution.EndTime == nil || !execution.EndTime.Equal(endTime) {
		t.Errorf("EndTime not set correctly")
	}
	if execution.Output != "test output" {
		t.Errorf("Output not set correctly")
	}
	if execution.Error != "" {
		t.Errorf("Error not set correctly")
	}
	if execution.ExitCode != 0 {
		t.Errorf("ExitCode not set correctly")
	}
}

func TestChainedScript_Structure(t *testing.T) {
	script := ChainedScript{
		Script: "test.sh",
		Args:   []string{"arg1", "arg2"},
		Filter: "trim",
	}

	if script.Script != "test.sh" {
		t.Errorf("Script not set correctly")
	}
	if len(script.Args) != 2 {
		t.Errorf("Args not set correctly")
	}
	if script.Filter != "trim" {
		t.Errorf("Filter not set correctly")
	}
}

func TestScriptChain_Structure(t *testing.T) {
	ctx := context.Background()

	chain := ScriptChain{
		Scripts: []ChainedScript{
			{Script: "script1.sh", Args: []string{"arg1"}},
			{Script: "script2.sh", Args: []string{"arg2"}, Filter: "first"},
		},
		Context: ctx,
	}

	if len(chain.Scripts) != 2 {
		t.Errorf("Expected 2 scripts in chain, got %d", len(chain.Scripts))
	}
	if chain.Scripts[0].Script != "script1.sh" {
		t.Errorf("First script not set correctly")
	}
	if chain.Scripts[1].Filter != "first" {
		t.Errorf("Second script filter not set correctly")
	}
	if chain.Context != ctx {
		t.Errorf("Context not set correctly")
	}
}

func TestApplyFilter(t *testing.T) {
	executor := &Executor{}

	testOutput := "line1\nline2\nline3\n  whitespace  \nline5"

	tests := []struct {
		filter   string
		expected string
	}{
		{"first", "line1"},
		{"last", "line5"},
		{"trim", "line1\nline2\nline3\n  whitespace  \nline5"},
		{"line2", "line2"},
		{"nonexistent", ""},
	}

	for _, test := range tests {
		t.Run(test.filter, func(t *testing.T) {
			result := executor.applyFilter(testOutput, test.filter)
			if result != test.expected {
				t.Errorf("Filter %s: expected %q, got %q", test.filter, test.expected, result)
			}
		})
	}
}

func TestGenerateExecutionID(t *testing.T) {
	id1 := generateExecutionID()
	time.Sleep(time.Nanosecond) // Ensure different timestamp
	id2 := generateExecutionID()

	if id1 == id2 {
		t.Errorf("Expected different execution IDs, both were: %s", id1)
	}

	if !strings.HasPrefix(id1, "exec_") {
		t.Errorf("Expected execution ID to start with 'exec_', got: %s", id1)
	}
	if !strings.HasPrefix(id2, "exec_") {
		t.Errorf("Expected execution ID to start with 'exec_', got: %s", id2)
	}
}

func TestNewExecutor(t *testing.T) {
	executor := NewExecutor(nil) // Using nil SSH client for testing

	if executor == nil {
		t.Errorf("Expected non-nil executor")
	}
	if executor.queue == nil {
		t.Errorf("Expected non-nil queue")
	}
	if executor.localScripts == nil {
		t.Errorf("Expected non-nil localScripts map")
	}
	if executor.remoteScripts == nil {
		t.Errorf("Expected non-nil remoteScripts map")
	}
	if executor.queue.maxSize != 100 {
		t.Errorf("Expected default queue size 100, got %d", executor.queue.maxSize)
	}
}
