package server

import (
	"context"
	"testing"
)

func TestCreateGoalsNilTask(t *testing.T) {
	if got := (&Server{}).createGoals(context.Background(), nil, nil); len(got) != 0 {
		t.Fatalf("nil task produced goals: %+v", got)
	}
}

func TestTaskExecutionContextPreservesNamedTarget(t *testing.T) {
	task := &Task{Name: "example.com scan", Description: "Use https://docs.vendor.net as reference", Goal: "Find exposed admin routes"}
	context := taskExecutionContext(task)
	if context != "example.com scan Use https://docs.vendor.net as reference" {
		t.Fatalf("task context = %q", context)
	}
	_, host, _, ok := parseTarget(taskSeedText(task))
	if !ok || host != "example.com" {
		t.Fatalf("seed target = %q, found = %v", host, ok)
	}
	task.Description = task.Name
	if got := taskExecutionContext(task); got != task.Name {
		t.Fatalf("task context with duplicate description = %q", got)
	}
}
