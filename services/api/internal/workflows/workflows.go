// Package workflows is the API's thin seam over the Temporal client: start a ticket workflow
// (idempotently) and signal human decisions.
package workflows

import (
	"context"
	"errors"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"github.com/alpine-dev/alpine/packages/shared/ticketflow"
)

// Engine starts and signals ticket workflows.
type Engine interface {
	// StartTicket starts TicketWorkflow with id ticketflow.WorkflowID(org, ticket). started=false
	// means a workflow with that id already exists (treated as success).
	StartTicket(ctx context.Context, in ticketflow.Input) (started bool, err error)
	Signal(ctx context.Context, workflowID string, d ticketflow.HumanDecision) error
	Ping(ctx context.Context) error
}

// Temporal implements Engine.
type Temporal struct {
	Client client.Client
}

// StartTicket implements Engine.
func (t Temporal) StartTicket(ctx context.Context, in ticketflow.Input) (bool, error) {
	_, err := t.Client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                       ticketflow.WorkflowID(in.OrgID, in.TicketID),
		TaskQueue:                ticketflow.TaskQueueWorkflows,
		WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
		SearchAttributes:         nil,
		Memo:                     map[string]any{"org_id": in.OrgID, "ticket_id": in.TicketID},
	}, ticketflow.WorkflowName, in)
	var already *serviceerror.WorkflowExecutionAlreadyStarted
	if errors.As(err, &already) {
		return false, nil
	}
	return err == nil, err
}

// Signal implements Engine.
func (t Temporal) Signal(ctx context.Context, workflowID string, d ticketflow.HumanDecision) error {
	return t.Client.SignalWorkflow(ctx, workflowID, "", ticketflow.SignalHumanDecision, d)
}

// Ping implements Engine.
func (t Temporal) Ping(ctx context.Context) error {
	_, err := t.Client.CheckHealth(ctx, &client.CheckHealthRequest{})
	return err
}

// ErrNotFound is returned by Signal when the workflow does not exist or has completed.
func IsNotFound(err error) bool {
	var nf *serviceerror.NotFound
	return errors.As(err, &nf)
}
