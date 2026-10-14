package main

import (
	"fmt"
	"log"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"

	"github.com/dimetron/ai-eng-course/labs/week2/internal/refund"
)

// newGraph composes the refund workflow this service serves over REST. It adapts
// the Day 3 explicit workflow graph topology:
//
//	Start → classify ─┬─ "refund"        → prepare → open_refund_case → format
//	                  ├─ "status"        → prepare_status → check_refund_status → format_status
//	                  └─ "out_of_domain" → refuse
//
// There is no merge node: each branch is terminal, which is what the engine
// expects of a leaf. The step functions and the tools come from
// week2/internal/refund.
//
// The classifier is deterministic by default (refund.Classify via classifyRouteText),
// and requires no model or API key. If reg carries a ClassifierModel, classifyRouteModel
// is used instead.
func newGraph(reg *refund.Registry) (agent.Agent, error) {
	if reg == nil {
		return nil, fmt.Errorf("refund registry is required")
	}

	refundTool, err := refund.NewTool(reg)
	if err != nil {
		return nil, fmt.Errorf("create refund tool: %w", err)
	}
	statusTool, err := refund.NewStatusTool(reg)
	if err != nil {
		return nil, fmt.Errorf("create status tool: %w", err)
	}
	// No retries: all work is local; validation errors cannot improve on retry.
	cfg := workflow.NodeConfig{}
	var classify *workflow.FunctionNode
	if c := reg.ClassifierModel(); c != nil {
		classify = workflow.NewEmittingFunctionNode("classify", classifyRouteModel(c), cfg)
	} else {
		classify = workflow.NewEmittingFunctionNode("classify", classifyRouteText, cfg)
	}
	prepare := workflow.NewFunctionNode("prepare", refund.Prepare, cfg)
	openCase, err := workflow.NewToolNodeTyped[refund.Input, refund.Output](refundTool, cfg)
	if err != nil {
		return nil, fmt.Errorf("create refund node: %w", err)
	}
	formatSchema := formatInputSchema()
	outputSchema, err := formatSchema.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("resolve refund output schema: %w", err)
	}
	// ADK requires equal schemas on both ends of an edge. Refine the typed
	// tool's output contract through its public BaseNode, keeping its inferred
	// input schema, identity and configuration unchanged.
	openCase.BaseNode = workflow.NewBaseNodeWithSchemas(
		openCase.Name(), openCase.Description(), openCase.Config(), openCase.InputSchema(), outputSchema,
	)
	format, err := workflow.NewFunctionNodeWithSchema[refund.Output, string](
		"format", refund.Format, formatSchema, nil, cfg,
	)
	if err != nil {
		return nil, fmt.Errorf("create format node: %w", err)
	}
	prepareStatus := workflow.NewFunctionNode("prepare_status", refund.PrepareStatus, cfg)
	checkStatus, err := workflow.NewToolNodeTyped[refund.StatusInput, refund.Output](statusTool, cfg)
	if err != nil {
		return nil, fmt.Errorf("create status node: %w", err)
	}
	formatStatus := workflow.NewFunctionNode("format_status", refund.FormatStatus, cfg)
	// The refusal is a declared step, not the absence of one: a request the
	// domain does not handle gets an answer in the log like every other.
	refuse := workflow.NewFunctionNode("refuse", refund.OutOfDomain, cfg)

	edges := workflow.Concat(
		workflow.Chain(workflow.Start, classify),
		[]workflow.Edge{
			{From: classify, To: prepare, Route: workflow.StringRoute(refund.RouteRefund)},
			{From: classify, To: prepareStatus, Route: workflow.StringRoute(refund.RouteStatus)},
			{From: classify, To: refuse, Route: workflow.StringRoute(refund.RouteOutOfDomain)},
		},
		workflow.Chain(prepare, openCase, format),
		workflow.Chain(prepareStatus, checkStatus, formatStatus),
	)
	return workflowagent.New(workflowagent.Config{
		Name:        refund.AppName,
		Description: "LEDGERWORKS: route the request, then open a refund case, read an existing one, or refuse.",
		Edges:       edges,
	})
}

// classifyRouteText emits the routing event and returns nil, which suppresses
// the node's own terminal event: the route is the only thing this node is for,
// and a second event carrying the same string would say nothing new.
//
// Output carries the user message into every branch. Without it the successor
// would receive a nil input and see an empty request instead of the one that
// was actually asked.
func classifyRouteText(ctx agent.Context, msg string, emit func(*session.Event) error) (any, error) {
	route, err := refund.Classify(ctx, msg)
	if err != nil {
		return nil, err
	}
	log.Printf("classify: rule %q → %s", msg, route)
	ev := session.NewEvent(ctx, ctx.InvocationID())
	ev.Routes = []string{route}
	ev.Output = msg
	if err := emit(ev); err != nil {
		return nil, err
	}
	return nil, nil
}

// routeInstruction is the whole prompt of the model classifier: one of three
// route names, nothing else. Ukrainian, because the request is read in
// Ukrainian and a language switch here is one more thing to be wrong.
const routeInstruction = `Ти маршрутизатор запитів LEDGERWORKS. Відповідай рівно одним словом:
refund — просять відкрити кейс повернення;
status — питають про вже відкритий кейс;
out_of_domain — усе інше.`

// classifyRouteModel is the alternative classifier: the same node body as
// classifyRouteText — same event, same route vocabulary — decided by a model
// instead of by refund.Classify.
func classifyRouteModel(c refund.Classifier) func(agent.Context, string, func(*session.Event) error) (any, error) {
	return func(ctx agent.Context, msg string, emit func(*session.Event) error) (any, error) {
		route, err := classifyWithModel(ctx, c, msg)
		if err != nil {
			return nil, err
		}
		log.Printf("classify: model %q → %s", msg, route)
		ev := session.NewEvent(ctx, ctx.InvocationID())
		ev.Routes = []string{route}
		ev.Output = msg
		if err := emit(ev); err != nil {
			return nil, err
		}
		return nil, nil
	}
}

// classifyWithModel asks for one route name and normalises the answer, never
// trusting it: a reply that is a whole sentence still lands on a branch the
// graph knows, because an unrecognised answer becomes out_of_domain rather than
// no route at all, which would dead-end the edge set above classify.
func classifyWithModel(ctx agent.Context, c refund.Classifier, msg string) (string, error) {
	req := &model.LLMRequest{
		Model:    c.Name(),
		Contents: []*genai.Content{genai.NewContentFromText(msg, genai.RoleUser)},
		Config: &genai.GenerateContentConfig{
			SystemInstruction: genai.NewContentFromText(routeInstruction, genai.RoleUser),
			Temperature:       genai.Ptr(float32(0)),
		},
	}
	var answer strings.Builder
	for resp, err := range c.GenerateContent(ctx, req, false) {
		if err != nil {
			return "", fmt.Errorf("classify with model: %w", err)
		}
		if resp.Content == nil {
			continue
		}
		for _, part := range resp.Content.Parts {
			answer.WriteString(part.Text)
		}
	}
	lowered := strings.ToLower(answer.String())
	for _, route := range []string{refund.RouteRefund, refund.RouteStatus} {
		if strings.Contains(lowered, route) {
			return route, nil
		}
	}
	return refund.RouteOutOfDomain, nil
}

// formatInputSchema defines the validation contract for the format node input (refund.Output).
// It verifies that CaseID, TransactionID, MerchantID, and Status are present and non-empty string fields.
func formatInputSchema() *jsonschema.Schema {
	minLen := 1
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"case_id":        {Type: "string", MinLength: &minLen},
			"transaction_id": {Type: "string", MinLength: &minLen},
			"merchant_id":    {Type: "string", MinLength: &minLen},
			"status":         {Type: "string", MinLength: &minLen},
		},
		Required: []string{"case_id", "transaction_id", "merchant_id", "status"},
	}
}
