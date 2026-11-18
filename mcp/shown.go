package mcp

import (
	"time"

	"github.com/mishmar-io/mishmar/api"
)

// What a tool prints of a decision. A tool's text is read by a language
// model, and through it by the app that runs the model, so it carries a
// chosen list of fields and not the decision record: the verdict and the
// order it is about, every rule that blocked or held the order, and what
// happens next (the approval, the signing, the venue's answer, a cancel).
//
// Left out on purpose: the signed submission (the venue message and its
// signing material, the nonce), both signatures, the Mishmar account's
// totals (state), the rules the order passed, the valuation's book detail,
// who decided an approval and their comment, and the record's bookkeeping.
// A field added to api.Decision reaches a model only when it is added here;
// TestOnlyTheChosenFieldsOfADecisionReachATool holds that.

// shownDecision is a decision as a tool prints it.
type shownDecision struct {
	ID                 string             `json:"id"`
	Verdict            api.Verdict        `json:"verdict"`
	Reason             *string            `json:"reason,omitempty"`
	CreatedAt          time.Time          `json:"createdAt"`
	AgentID            string             `json:"agentId"`
	AgentName          string             `json:"agentName"`
	ConnectedAccountID string             `json:"connectedAccountId"`
	Exit               *bool              `json:"exit,omitempty"`
	Proposal           shownProposal      `json:"proposal"`
	RulesNotPassed     []shownRule        `json:"rulesNotPassed"`
	ApprovalID         *string            `json:"approvalId,omitempty"`
	Approval           *shownOutcome      `json:"approval,omitempty"`
	Signing            *shownSigning      `json:"signing,omitempty"`
	VenueAck           *shownVenueAck     `json:"venueAck,omitempty"`
	Cancellation       *shownCancellation `json:"cancellation,omitempty"`
}

// shownProposal is the order or the cancel the decision is about.
type shownProposal struct {
	Kind                api.ProposalKind      `json:"kind"`
	Venue               api.Venue             `json:"venue"`
	Symbol              string                `json:"symbol"`
	Side                api.Side              `json:"side"`
	Size                api.Decimal           `json:"size"`
	OrderType           api.ProposalOrderType `json:"orderType"`
	Price               *api.Decimal          `json:"price,omitempty"`
	ReduceOnly          bool                  `json:"reduceOnly"`
	Leverage            *api.Decimal          `json:"leverage,omitempty"`
	NotionalUsd         api.Decimal           `json:"notionalUsd"`
	ClientOrderID       string                `json:"clientOrderId"`
	TargetClientOrderID *string               `json:"targetClientOrderId,omitempty"`
	TargetDecisionID    *string               `json:"targetDecisionId,omitempty"`
	ExpiresAt           time.Time             `json:"expiresAt"`
}

// shownRule is a rule the order did not pass: one that blocked it or held
// it for approval, wherever the rule is set.
type shownRule struct {
	RuleID    string          `json:"ruleId"`
	RuleName  string          `json:"ruleName"`
	Scope     api.Scope       `json:"scope"`
	Metric    api.RuleMetric  `json:"metric"`
	Observed  api.Decimal     `json:"observed"`
	Threshold api.Decimal     `json:"threshold"`
	Unit      api.RuleUnit    `json:"unit"`
	Outcome   api.RuleOutcome `json:"outcome"`
}

// shownOutcome is where a parked order's approval stands.
type shownOutcome struct {
	ID          string             `json:"id"`
	Status      api.ApprovalStatus `json:"status"`
	RequestedAt time.Time          `json:"requestedAt"`
	ExpiresAt   time.Time          `json:"expiresAt"`
	DecidedAt   *time.Time         `json:"decidedAt,omitempty"`
}

type shownSigning struct {
	Status    api.SigningStatusStatus `json:"status"`
	Detail    *string                 `json:"detail,omitempty"`
	WaitUntil *time.Time              `json:"waitUntil,omitempty"`
}

type shownVenueAck struct {
	Status        string    `json:"status"`
	OrderID       string    `json:"orderId"`
	ClientOrderID *string   `json:"clientOrderId,omitempty"`
	ReceivedAt    time.Time `json:"receivedAt"`
}

type shownCancellation struct {
	At               time.Time               `json:"at"`
	By               api.OrderCancellationBy `json:"by"`
	CancelDecisionID *string                 `json:"cancelDecisionId,omitempty"`
	FilledSize       api.Decimal             `json:"filledSize"`
	VenueStatus      *string                 `json:"venueStatus,omitempty"`
}

// shownDecisionPage is list_decisions' answer.
type shownDecisionPage struct {
	Items      []shownDecision `json:"items"`
	NextCursor *string         `json:"nextCursor,omitempty"`
}

// shownApproval is one of list_approvals' answers: the approval, its
// decision, and how the order would be judged now.
type shownApproval struct {
	ID          string             `json:"id"`
	DecisionID  string             `json:"decisionId"`
	Status      api.ApprovalStatus `json:"status"`
	RequestedAt time.Time          `json:"requestedAt"`
	ExpiresAt   time.Time          `json:"expiresAt"`
	DecidedAt   *time.Time         `json:"decidedAt,omitempty"`
	Decision    shownDecision      `json:"decision"`
	Now         *shownNow          `json:"now,omitempty"`
}

// shownNow is the order judged again as things stand.
type shownNow struct {
	EvaluatedAt    time.Time   `json:"evaluatedAt"`
	Verdict        api.Verdict `json:"verdict"`
	Reason         *string     `json:"reason,omitempty"`
	RulesNotPassed []shownRule `json:"rulesNotPassed"`
}

// notPassed is the evaluations that blocked or held the order: not passed
// and not waived, as describeDecision's sentence names them.
func notPassed(evaluations []api.RuleEvaluation) []shownRule {
	out := []shownRule{}
	for _, e := range evaluations {
		if e.Passed || (e.Waived != nil && *e.Waived) {
			continue
		}
		out = append(out, shownRule{
			RuleID: e.RuleId, RuleName: e.RuleName, Scope: api.Scope{Id: e.Scope.Id, Kind: e.Scope.Kind},
			Metric: e.Metric, Observed: e.Observed, Threshold: e.Threshold, Unit: e.Unit, Outcome: e.Outcome,
		})
	}
	return out
}

func showDecision(d api.Decision) shownDecision {
	p := d.Proposal
	out := shownDecision{
		ID: d.Id, Verdict: d.Verdict, Reason: d.Reason, CreatedAt: d.CreatedAt,
		AgentID: d.AgentId, AgentName: d.AgentName, ConnectedAccountID: d.ConnectedAccountId, Exit: d.Exit,
		Proposal: shownProposal{
			Kind: p.Kind, Venue: p.Venue, Symbol: p.Symbol, Side: p.Side, Size: p.Size, OrderType: p.OrderType,
			Price: p.Price, ReduceOnly: p.ReduceOnly, Leverage: p.Leverage, NotionalUsd: p.NotionalUsd,
			ClientOrderID: p.ClientOrderId, TargetClientOrderID: p.TargetClientOrderId,
			TargetDecisionID: p.TargetDecisionId, ExpiresAt: p.ExpiresAt,
		},
		RulesNotPassed: notPassed(d.Evaluations),
		ApprovalID:     d.ApprovalId,
	}
	if a := d.Approval; a != nil {
		out.Approval = &shownOutcome{ID: a.Id, Status: a.Status, RequestedAt: a.RequestedAt, ExpiresAt: a.ExpiresAt, DecidedAt: a.DecidedAt}
	}
	if s := d.Signing; s != nil {
		out.Signing = &shownSigning{Status: s.Status, Detail: s.Detail, WaitUntil: s.WaitUntil}
	}
	if v := d.VenueAck; v != nil {
		out.VenueAck = &shownVenueAck{Status: v.Status, OrderID: v.OrderId, ClientOrderID: v.ClientOrderId, ReceivedAt: v.ReceivedAt}
	}
	if c := d.Cancellation; c != nil {
		out.Cancellation = &shownCancellation{At: c.At, By: c.By, CancelDecisionID: c.CancelDecisionId, FilledSize: c.FilledSize, VenueStatus: c.VenueStatus}
	}
	return out
}

func showDecisionPage(page api.DecisionPage) shownDecisionPage {
	out := shownDecisionPage{Items: make([]shownDecision, 0, len(page.Items)), NextCursor: page.NextCursor}
	for _, d := range page.Items {
		out.Items = append(out.Items, showDecision(d))
	}
	return out
}

func showApprovals(approvals []api.Approval) []shownApproval {
	out := make([]shownApproval, 0, len(approvals))
	for _, a := range approvals {
		s := shownApproval{
			ID: a.Id, DecisionID: a.DecisionId, Status: a.Status, RequestedAt: a.RequestedAt,
			ExpiresAt: a.ExpiresAt, DecidedAt: a.DecidedAt, Decision: showDecision(a.Decision),
		}
		if n := a.Now; n != nil {
			s.Now = &shownNow{EvaluatedAt: n.EvaluatedAt, Verdict: n.Verdict, Reason: n.Reason, RulesNotPassed: notPassed(n.Evaluations)}
		}
		out = append(out, s)
	}
	return out
}
