package mcp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/api"
)

// marks fills a value of any shape with values that can be found again in a
// text: every string, number and time under it gets one of its own, named
// for where it sits (Proposal.Symbol, Evaluations[0].RuleName). Bools are set
// true and carry no mark. A field added to the API's types is filled like
// the rest, with no change here.
type marks struct {
	at map[string]string // path -> the text its value prints as
	n  int
}

var (
	timeType = reflect.TypeOf(time.Time{})
	rawType  = reflect.TypeOf(json.RawMessage{})
)

func (m *marks) fill(v reflect.Value, path string) {
	switch v.Kind() {
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		m.fill(v.Elem(), path)
	case reflect.String:
		mark := "mk:" + path + ":km"
		v.SetString(mark)
		m.at[path] = mark
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		m.n++
		v.SetInt(int64(7_000_000 + m.n))
		m.at[path] = fmt.Sprint(7_000_000 + m.n)
	case reflect.Float32, reflect.Float64:
		m.n++
		v.SetFloat(float64(7_000_000+m.n) + 0.5)
		m.at[path] = fmt.Sprint(7_000_000+m.n) + ".5"
	case reflect.Struct:
		if v.Type() == timeType {
			m.n++
			t := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(m.n) * time.Hour)
			v.Set(reflect.ValueOf(t))
			m.at[path] = t.Format(time.RFC3339)
			return
		}
		for i := 0; i < v.NumField(); i++ {
			if f := v.Type().Field(i); f.IsExported() {
				m.fill(v.Field(i), strings.TrimPrefix(path+"."+f.Name, "."))
			}
		}
	case reflect.Slice:
		mark := "mk:" + path + ":km"
		switch {
		case v.Type() == rawType:
			v.SetBytes([]byte(`"` + mark + `"`))
			m.at[path] = mark
		case v.Type().Elem().Kind() == reflect.Uint8:
			v.SetBytes([]byte(mark))
			m.at[path] = base64.StdEncoding.EncodeToString([]byte(mark))
		default:
			v.Set(reflect.MakeSlice(v.Type(), 1, 1))
			m.fill(v.Index(0), path+"[0]")
		}
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		e := reflect.New(v.Type().Elem()).Elem()
		m.fill(e, path+"[k]")
		k := reflect.New(v.Type().Key()).Elem()
		if k.Kind() == reflect.String {
			k.SetString("k")
		}
		v.SetMapIndex(k, e)
	case reflect.Interface:
		if v.NumMethod() == 0 {
			mark := "mk:" + path + ":km"
			v.Set(reflect.ValueOf(mark))
			m.at[path] = mark
		}
	}
}

// The fields of a decision a tool prints, by where they sit in api.Decision.
var shownOfDecision = []string{
	"Id", "Verdict", "Reason", "CreatedAt", "AgentId", "AgentName", "ConnectedAccountId",
	"Proposal.Kind", "Proposal.Venue", "Proposal.Symbol", "Proposal.Side", "Proposal.Size", "Proposal.OrderType",
	"Proposal.Price", "Proposal.Leverage", "Proposal.NotionalUsd", "Proposal.ClientOrderId",
	"Proposal.TargetClientOrderId", "Proposal.TargetDecisionId", "Proposal.ExpiresAt",
	"ApprovalId", "Approval.Id", "Approval.Status", "Approval.RequestedAt", "Approval.ExpiresAt", "Approval.DecidedAt",
	"Signing.Status", "Signing.Detail", "Signing.WaitUntil",
	"VenueAck.Status", "VenueAck.OrderId", "VenueAck.ClientOrderId", "VenueAck.ReceivedAt",
	"Cancellation.At", "Cancellation.By", "Cancellation.CancelDecisionId", "Cancellation.FilledSize", "Cancellation.VenueStatus",
}

// The fields of a rule the order did not pass.
var shownOfRule = []string{"RuleId", "RuleName", "Scope.Id", "Scope.Kind", "Metric", "Observed", "Threshold", "Unit", "Outcome"}

func under(prefix string, paths ...string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = prefix + p
	}
	return out
}

// threeRules makes the first evaluation one the order did not pass, and adds
// one it passed and one that was waived, each with marks of its own.
func threeRules(m *marks, evaluations *[]api.RuleEvaluation, path string) {
	(*evaluations)[0].Passed, (*evaluations)[0].Waived, (*evaluations)[0].WaivedReason = false, nil, nil
	delete(m.at, path+"[0].WaivedReason")
	var passed, waived api.RuleEvaluation
	m.fill(reflect.ValueOf(&passed).Elem(), path+"[passed]")
	m.fill(reflect.ValueOf(&waived).Elem(), path+"[waived]")
	waived.Passed = false
	*evaluations = append(*evaluations, passed, waived)
}

// check holds text to the marks named in shown: each of those is in it, and
// no other mark is.
func check(t *testing.T, what, text string, m *marks, shown []string) {
	t.Helper()
	want := map[string]bool{}
	for _, p := range shown {
		if _, ok := m.at[p]; !ok {
			t.Errorf("%s: %s is on the list of what is shown and is no field of the record", what, p)
		}
		want[p] = true
	}
	var leaked, missing []string
	for p, mark := range m.at {
		switch in := strings.Contains(text, mark); {
		case in && !want[p]:
			leaked = append(leaked, p)
		case !in && want[p]:
			missing = append(missing, p)
		}
	}
	sort.Strings(leaked)
	sort.Strings(missing)
	if len(leaked) > 0 {
		t.Errorf("%s prints fields that are not on the list of what a tool shows: %v", what, leaked)
	}
	if len(missing) > 0 {
		t.Errorf("%s does not print fields that are on the list: %v", what, missing)
	}
}

// A tool prints a chosen list of a decision's fields. Everything else in the
// record stays out of its text, whatever the record gains later: the signed
// submission and its signing material, the signatures, the account's totals,
// the rules the order passed.
func TestOnlyTheChosenFieldsOfADecisionReachATool(t *testing.T) {
	filled := func() (api.Decision, *marks, []string) {
		m := &marks{at: map[string]string{}}
		var d api.Decision
		m.fill(reflect.ValueOf(&d).Elem(), "")
		threeRules(m, &d.Evaluations, "Evaluations")
		return d, m, append(append([]string{}, shownOfDecision...), under("Evaluations[0].", shownOfRule...)...)
	}

	d, m, shown := filled()
	if len(m.at) < 80 || m.at["Submission.VenueMessage.Share"] == "" || m.at["State.GrossExposureUsd"] == "" || m.at["AgentSignature"] == "" {
		t.Fatalf("the record was not filled: %d marks", len(m.at))
	}
	text, err := describeDecision(d)
	if err != nil {
		t.Fatal(err)
	}
	check(t, "a decision tool", text, m, shown)

	d, m, shown = filled()
	next := "mk:NextCursor:km"
	text, err = asJSON(showDecisionPage(api.DecisionPage{Items: []api.Decision{d}, NextCursor: &next}))
	if err != nil {
		t.Fatal(err)
	}
	check(t, "list_decisions", text, m, shown)
	if !strings.Contains(text, next) {
		t.Errorf("list_decisions lost the cursor: %s", text)
	}
}

func TestOnlyTheChosenFieldsOfAnApprovalReachATool(t *testing.T) {
	m := &marks{at: map[string]string{}}
	var a api.Approval
	m.fill(reflect.ValueOf(&a).Elem(), "")
	threeRules(m, &a.Decision.Evaluations, "Decision.Evaluations")
	threeRules(m, &a.Now.Evaluations, "Now.Evaluations")
	if m.at["Now.State.GrossExposureUsd"] == "" || m.at["Decision.Submission.Nonce"] == "" || m.at["DecidedBy"] == "" {
		t.Fatalf("the record was not filled: %d marks", len(m.at))
	}
	shown := []string{"Id", "DecisionId", "Status", "RequestedAt", "ExpiresAt", "DecidedAt", "Now.EvaluatedAt", "Now.Verdict", "Now.Reason"}
	shown = append(shown, under("Decision.", shownOfDecision...)...)
	shown = append(shown, under("Decision.Evaluations[0].", shownOfRule...)...)
	shown = append(shown, under("Now.Evaluations[0].", shownOfRule...)...)
	text, err := asJSON(showApprovals([]api.Approval{a}))
	if err != nil {
		t.Fatal(err)
	}
	check(t, "list_approvals", text, m, shown)
}

// The tools' answers through the server: the text of every tool that prints
// a decision holds nothing of the submission, the signatures or the
// account's totals, and still names the decision.
func TestAToolsAnswerHoldsNoSigningMaterial(t *testing.T) {
	m := &marks{at: map[string]string{}}
	var d api.Decision
	m.fill(reflect.ValueOf(&d).Elem(), "")
	am := &marks{at: map[string]string{}}
	var a api.Approval
	am.fill(reflect.ValueOf(&a).Elem(), "")
	s := start(t, &fakeBackend{decision: d, approvals: []api.Approval{a}})

	kept := []string{"Submission.VenueMessage.Share", "Submission.VenueMessage.Message", "Submission.VenueMessage.EngineCommitmentId", "Submission.Nonce", "AgentSignature", "EngineSignature", "State.GrossExposureUsd", "State.DailyPnlUsd"}
	for _, call := range []map[string]any{
		{"name": "propose_order", "arguments": map[string]any{"connectedAccountId": "ta_1", "symbol": "BTC", "side": "buy", "size": "0.001", "orderType": "market"}},
		{"name": "cancel_order", "arguments": map[string]any{"connectedAccountId": "ta_1", "symbol": "BTC", "clientOrderId": "msh-1"}},
		{"name": "get_decision", "arguments": map[string]any{"decisionId": "dec_1"}},
		{"name": "list_decisions"},
	} {
		txt, isErr := text(t, s.call("tools/call", call))
		if isErr {
			t.Fatalf("%s: %s", call["name"], txt)
		}
		for _, p := range kept {
			if m.at[p] == "" {
				t.Fatalf("no mark for %s", p)
			}
			if strings.Contains(txt, m.at[p]) {
				t.Errorf("%s's answer holds %s", call["name"], p)
			}
		}
		if !strings.Contains(txt, m.at["Id"]) || !strings.Contains(txt, m.at["Proposal.Venue"]) {
			t.Errorf("%s's answer lost the decision: %s", call["name"], txt)
		}
	}

	txt, isErr := text(t, s.call("tools/call", map[string]any{"name": "list_approvals"}))
	if isErr {
		t.Fatalf("list_approvals: %s", txt)
	}
	for _, p := range append(under("Decision.", kept...), "Now.State.GrossExposureUsd", "DecidedBy", "Comment") {
		if am.at[p] == "" {
			t.Fatalf("no mark for %s", p)
		}
		if strings.Contains(txt, am.at[p]) {
			t.Errorf("list_approvals' answer holds %s", p)
		}
	}
	if !strings.Contains(txt, am.at["Id"]) || !strings.Contains(txt, am.at["Decision.Id"]) {
		t.Errorf("list_approvals' answer lost the approval: %s", txt)
	}
}
