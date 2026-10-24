// Command reply is where a person's answer comes back in. One handler serves
// two callers, told apart by the shape of the invocation:
//
//   - API Gateway: GET /reply renders the decision page and changes nothing,
//     because mail scanners fetch links before people do. POST /reply records
//     the answer and hands it to the waiting execution.
//   - The state machine: "arm" stores the task token once the execution is
//     holding, and "release", "record_denial", and "record_timeout" write the
//     receipt for how the hold ended. "record_broken" writes the receipt for
//     a stage that failed after its retries, so the execution does not end
//     with nothing saying why.
//
// A held action is released by a person's answer and nothing else: not a
// timeout, not a default, not a click this handler had to guess about.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	"github.com/aws/smithy-go"

	"github.com/aisolutions/reachee/internal/audit"
	"github.com/aisolutions/reachee/internal/config"
	"github.com/aisolutions/reachee/internal/ddb"
	"github.com/aisolutions/reachee/internal/replytoken"
	"github.com/aisolutions/reachee/internal/signing"
)

// signingKey is fetched once, at init, and reused by every invocation this
// execution environment serves.
var signingKey ed25519.PrivateKey

// Decisions a person can make. Anything else is not a decision.
const (
	decisionDirection = "direction"
	decisionDenial    = "denial"
)

// maxAnswer bounds the free text. A decision fits; a document does not.
const maxAnswer = 2000

// TaskSender is the one Step Functions call reply makes.
type TaskSender interface {
	SendTaskSuccess(ctx context.Context, in *sfn.SendTaskSuccessInput, optFns ...func(*sfn.Options)) (*sfn.SendTaskSuccessOutput, error)
}

type replier struct {
	db       ddb.Client
	sfn      TaskSender
	table    string
	key      ed25519.PrivateKey
	replyKey []byte
	now      func() time.Time
}

// shape is just enough of an invocation to tell who sent it. API Gateway
// always sets routeKey; the state machine's payloads never do.
type shape struct {
	RouteKey string `json:"routeKey"`
}

// stateInput is what the state machine's Task states send.
type stateInput struct {
	Action       string  `json:"action"`
	EscalationID string  `json:"escalation_id"`
	TaskToken    string  `json:"task_token"`
	Answer       *answer `json:"answer"`
	Stage        string  `json:"stage"`
	Error        string  `json:"error"`
	Tier         string  `json:"tier"`
}

// answer is the SendTaskSuccess output: the classification and a hash of the
// person's words, never the words themselves, since it lands in the
// execution history.
type answer struct {
	Decision   string `json:"decision"`
	Confidence string `json:"confidence"`
	AnswerHash string `json:"answer_hash"`
}

func (h *replier) handle(ctx context.Context, raw json.RawMessage) (any, error) {
	var s shape
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("reply: invocation is not JSON: %w", err)
	}
	if s.RouteKey != "" {
		var ev events.APIGatewayV2HTTPRequest
		if err := json.Unmarshal(raw, &ev); err != nil {
			return nil, err
		}
		return h.http(ctx, ev), nil
	}
	var in stateInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	if in.EscalationID == "" {
		return nil, errors.New("reply: state input has no escalation_id")
	}
	switch in.Action {
	case "arm":
		return nil, h.arm(ctx, in)
	case "release":
		return nil, h.release(ctx, in)
	case "record_denial":
		return nil, h.recordDenial(ctx, in)
	case "record_timeout":
		return nil, h.recordTimeout(ctx, in)
	case "record_broken":
		return nil, h.recordBroken(ctx, in)
	default:
		return nil, fmt.Errorf("reply: unsupported action %q", in.Action)
	}
}

// arm stores the task token and returns. It runs after the message is
// already out, which is why a person can, in principle, answer first.
func (h *replier) arm(ctx context.Context, in stateInput) error {
	if in.TaskToken == "" {
		return errors.New("reply: arm without a task token")
	}
	return ddb.ArmReply(ctx, h.db, h.table, in.EscalationID, in.TaskToken, h.now())
}

// release writes RESOLVED. The state machine reaches it only through a
// direction, and it checks the stored decision as well, so a definition
// edited to route wrongly fails here instead of releasing.
func (h *replier) release(ctx context.Context, in stateInput) error {
	slot, err := ddb.GetReply(ctx, h.db, h.table, in.EscalationID)
	if err != nil {
		return err
	}
	if slot.Decision != decisionDirection || in.Answer == nil || in.Answer.Decision != decisionDirection {
		return fmt.Errorf("reply: refusing to release %s: recorded decision is %q", in.EscalationID, slot.Decision)
	}
	return h.emitFor(ctx, in.EscalationID, audit.TypeResolved, in.Answer.AnswerHash, map[string]string{
		"released":   "true",
		"intent":     in.Answer.Decision,
		"confidence": in.Answer.Confidence,
	})
}

// recordDenial writes DENIED. The state machine routes every answer that is
// not a direction here, so the reason says which it was.
func (h *replier) recordDenial(ctx context.Context, in stateInput) error {
	meta := map[string]string{"released": "false"}
	hash := ""
	if in.Answer != nil {
		meta["intent"] = in.Answer.Decision
		meta["confidence"] = in.Answer.Confidence
		hash = in.Answer.AnswerHash
	}
	if in.Answer == nil || in.Answer.Decision != decisionDenial {
		meta["reason"] = "answer was not a recognized direction; nothing released"
	} else {
		meta["reason"] = "the person denied the held action"
	}
	return h.emitFor(ctx, in.EscalationID, audit.TypeDenied, hash, meta)
}

// recordTimeout closes the window first, so a link clicked a moment later is
// refused rather than recorded against an execution that stopped waiting.
func (h *replier) recordTimeout(ctx context.Context, in stateInput) error {
	if err := ddb.CloseReply(ctx, h.db, h.table, in.EscalationID, "timeout", h.now()); err != nil {
		return err
	}
	return h.emitFor(ctx, in.EscalationID, audit.TypeTimedOut, "", map[string]string{
		"released": "false",
		"reason":   "no answer within the declared window; a timeout is not consent",
	})
}

// brokenStages are the Task states whose Catch routes here. The stage arrives
// as a literal from the definition, but it is still checked: it is written
// into a signed receipt.
var brokenStages = map[string]bool{"Attempt": true, "AwaitAnswer": true, "Release": true, "Denied": true, "TimedOut": true}

// errorName matches the error names Step Functions and Lambda report, such as
// Lambda.TooManyRequestsException. The Catch's Cause is free text and may
// carry a function's error message, so it is never passed in.
var errorName = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,100}$`)

// recordBroken writes FAILED for a stage that ended in an error after its
// retries. It closes the reply window first, so a link clicked later is
// refused instead of waiting on an execution that has stopped. A chain that
// already ends in a terminal receipt is left alone: a stage can fail after
// writing its receipt, and a second terminal receipt would contradict it.
func (h *replier) recordBroken(ctx context.Context, in stateInput) error {
	if !brokenStages[in.Stage] {
		return fmt.Errorf("reply: record_broken for unknown stage %q", in.Stage)
	}
	if err := ddb.CloseReply(ctx, h.db, h.table, in.EscalationID, "failed", h.now()); err != nil {
		return err
	}
	chain, err := ddb.ReadChain(ctx, h.db, h.table, in.EscalationID)
	if err != nil {
		return err
	}
	if n := len(chain); n > 0 && terminal(chain[n-1]) {
		log.Printf("reply: %s already ends in %s; %s failure not recorded over it", in.EscalationID, chain[n-1].EventType, in.Stage)
		return nil
	}
	name := in.Error
	if !errorName.MatchString(name) {
		name = "unrecognized"
	}
	meta := map[string]string{
		"released": "false",
		"stage":    in.Stage,
		"error":    name,
		"reason":   "promise broken: " + in.Stage + " could not complete; nothing was released",
	}
	if in.Stage == "Attempt" && in.Tier != "" {
		meta["tier"] = in.Tier
	}
	return h.emitFor(ctx, in.EscalationID, audit.TypeFailed, "", meta)
}

// terminal reports whether e ends a hold. A FAILED receipt is terminal only
// when it carries released; a tier's FAILED does not.
func terminal(e audit.Event) bool {
	switch e.EventType {
	case audit.TypeResolved, audit.TypeDenied, audit.TypeTimedOut:
		return true
	case audit.TypeFailed:
		_, ok := e.Metadata["released"]
		return ok
	}
	return false
}

func (h *replier) emitFor(ctx context.Context, escalationID, eventType, hash string, meta map[string]string) error {
	req, err := ddb.GetRequest(ctx, h.db, h.table, escalationID)
	if err != nil {
		return err
	}
	_, err = h.emit(ctx, req, eventType, hash, meta)
	return err
}

func (h *replier) emit(ctx context.Context, req ddb.Request, eventType, hash string, meta map[string]string) (audit.Event, error) {
	sink, err := ddb.NewSink(ctx, h.db, h.table, req.EscalationID)
	if err != nil {
		return audit.Event{}, err
	}
	return audit.NewLogger(h.key, sink, h.now).Emit(audit.Event{
		RequestID:   req.EscalationID,
		RecipientID: req.RecipientID,
		EventType:   eventType,
		MessageHash: hash,
		Metadata:    meta,
	})
}

func (h *replier) http(ctx context.Context, ev events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	switch ev.RouteKey {
	case "GET /reply":
		return h.show(ctx, ev.QueryStringParameters["t"])
	case "POST /reply":
		form, err := formOf(ev)
		if err != nil {
			return page(http.StatusBadRequest, view{Title: "That did not come through", Message: "The form could not be read. Open the link from the message again."})
		}
		return h.submit(ctx, form)
	default:
		return page(http.StatusNotFound, view{Title: "Not found"})
	}
}

// open checks a link and loads what it points at. It writes nothing.
func (h *replier) open(ctx context.Context, token string) (replytoken.Claims, ddb.Request, ddb.Reply, *events.APIGatewayV2HTTPResponse) {
	claims, err := replytoken.Verify(h.replyKey, token, h.now())
	if errors.Is(err, replytoken.ErrExpired) {
		r := page(http.StatusGone, view{Title: "This link has expired", Message: "The window to answer has closed. Nothing was recorded from this link."})
		return claims, ddb.Request{}, ddb.Reply{}, &r
	}
	if err != nil {
		r := page(http.StatusForbidden, view{Title: "This link is not valid", Message: "Open the link exactly as it appears in the message."})
		return claims, ddb.Request{}, ddb.Reply{}, &r
	}
	req, err := ddb.GetRequest(ctx, h.db, h.table, claims.EscalationID)
	if err != nil || req.RecipientID != claims.RecipientID {
		r := page(http.StatusForbidden, view{Title: "This link is not valid", Message: "It does not match an open escalation."})
		return claims, req, ddb.Reply{}, &r
	}
	slot, err := ddb.GetReply(ctx, h.db, h.table, claims.EscalationID)
	if err != nil {
		r := page(http.StatusInternalServerError, view{Title: "Something went wrong", Message: "Nothing was recorded. Try the link again."})
		return claims, req, slot, &r
	}
	if r := refusal(slotState(slot, claims.Nonce)); r != nil {
		return claims, req, slot, r
	}
	return claims, req, slot, nil
}

func (h *replier) show(ctx context.Context, token string) events.APIGatewayV2HTTPResponse {
	_, req, _, refused := h.open(ctx, token)
	if refused != nil {
		return *refused
	}
	return page(http.StatusOK, decide(req, token, ""))
}

// submit records one answer. It classifies before it spends the nonce, so an
// answer it cannot read with confidence is handed back to the person with
// the link still good, rather than guessed at.
func (h *replier) submit(ctx context.Context, form url.Values) events.APIGatewayV2HTTPResponse {
	token := form.Get("t")
	claims, req, _, refused := h.open(ctx, token)
	if refused != nil {
		return *refused
	}

	text := strings.TrimSpace(form.Get("text"))
	if len(text) > maxAnswer {
		return page(http.StatusUnprocessableEntity, decide(req, token, fmt.Sprintf("Keep the note under %d characters. Nothing was recorded.", maxAnswer)))
	}
	intent, confidence, err := classify(form.Get("decision"), text)
	if err != nil {
		return page(http.StatusUnprocessableEntity, decide(req, token, err.Error()+" Nothing was recorded; choose again."))
	}

	slot, err := ddb.SpendReply(ctx, h.db, h.table, req.EscalationID, claims.Nonce, intent, text, h.now())
	if r := refusal(err); r != nil {
		return *r
	}
	if err != nil {
		return page(http.StatusInternalServerError, view{Title: "Something went wrong", Message: "Nothing was recorded. Try the link again."})
	}

	hash := audit.HashContent(text)
	if _, err := h.emit(ctx, req, audit.TypeReceived, hash, map[string]string{"via": "reply link"}); err != nil {
		return page(http.StatusInternalServerError, view{Title: "Your answer was not handed on", Message: "It could not be recorded. Nothing has been released."})
	}
	if _, err := h.emit(ctx, req, audit.TypeAcknowledged, hash, map[string]string{
		"intent": intent, "confidence": confidence, "classifier": "button and text, rules v1",
	}); err != nil {
		return page(http.StatusInternalServerError, view{Title: "Your answer was not handed on", Message: "It could not be classified on the record. Nothing has been released."})
	}

	output, _ := json.Marshal(answer{Decision: intent, Confidence: confidence, AnswerHash: hash})
	if _, err := h.sfn.SendTaskSuccess(ctx, &sfn.SendTaskSuccessInput{
		TaskToken: aws.String(slot.TaskToken),
		Output:    aws.String(string(output)),
	}); err != nil {
		// The answer is on the record but the hold did not take it. Report
		// the broken hand-off rather than a decision that went nowhere.
		code := "transport error"
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			code = apiErr.ErrorCode()
		}
		_, _ = h.emit(ctx, req, audit.TypeFailed, hash, map[string]string{
			"reason": "answer recorded but the execution did not accept it: " + code,
		})
		return page(http.StatusBadGateway, view{Title: "Your answer was recorded but not handed on", Message: "Reachee could not pass it to the waiting agent. Nothing has been released."})
	}

	if intent == decisionDirection {
		return page(http.StatusOK, view{Title: "Recorded", Message: "Your direction has been passed to the agent. You can close this page."})
	}
	return page(http.StatusOK, view{Title: "Recorded", Message: "The held action will not go ahead. You can close this page."})
}

// slotState says whether a link may still be used, as an error or nil.
// Readiness is not checked here: an unarmed slot can still be shown, and
// SpendReply refuses it at the moment of answering.
func slotState(slot ddb.Reply, nonce string) error {
	switch {
	case slot.Nonce != nonce:
		return ddb.ErrNonceMismatch
	case !slot.SpentAt.IsZero():
		return ddb.ErrSpent
	case !slot.ClosedAt.IsZero():
		return ddb.ErrClosed
	}
	return nil
}

// refusal turns a slot refusal into the page the person sees, or nil.
func refusal(err error) *events.APIGatewayV2HTTPResponse {
	var r events.APIGatewayV2HTTPResponse
	switch {
	case errors.Is(err, ddb.ErrNotArmed):
		r = page(http.StatusConflict, view{Title: "Not ready yet", Message: "Reachee is not holding for your answer yet. Nothing was recorded. Go back and try again in a moment."})
	case errors.Is(err, ddb.ErrSpent):
		r = page(http.StatusGone, view{Title: "Already answered", Message: "This escalation already has an answer. Each link records one."})
	case errors.Is(err, ddb.ErrClosed):
		r = page(http.StatusGone, view{Title: "The window has closed", Message: "No answer was recorded in time, and none can be now."})
	case errors.Is(err, ddb.ErrNonceMismatch):
		r = page(http.StatusGone, view{Title: "This link has been replaced", Message: "Use the most recent message about this escalation."})
	default:
		return nil
	}
	return &r
}

// Words that, in a note, contradict the button pressed. The list is short and
// explicit on purpose: "hold" or "do not give" can be a direction, so only
// words about the decision itself count.
var (
	refusalWords  = []string{"deny", "denied", "reject", "rejected", "refuse", "refused", "cancel", "abort"}
	approvalWords = []string{"approve", "approved", "go ahead", "proceed"}
)

// classify reads intent from the button, with the note as a check on it. The
// button is the decision; a note that contradicts it means the answer cannot
// be read with confidence, and that is handed back rather than guessed.
func classify(button, text string) (intent, confidence string, err error) {
	if button != decisionDirection && button != decisionDenial {
		return "", "", errors.New("Choose one of the two buttons.")
	}
	lower := " " + strings.ToLower(text) + " "
	has := func(words []string) bool {
		for _, w := range words {
			if strings.Contains(lower, w) {
				return true
			}
		}
		return false
	}
	if button == decisionDirection && has(refusalWords) {
		return "", "", errors.New("Your note reads like a refusal, but you pressed Give direction.")
	}
	if button == decisionDenial && has(approvalWords) {
		return "", "", errors.New("Your note reads like an approval, but you pressed Deny.")
	}
	if text == "" {
		return button, "button", nil
	}
	return button, "button and note agree", nil
}

func formOf(ev events.APIGatewayV2HTTPRequest) (url.Values, error) {
	body := ev.Body
	if ev.IsBase64Encoded {
		b, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			return nil, err
		}
		body = string(b)
	}
	if len(body) > 16<<10 {
		return nil, errors.New("form too large")
	}
	return url.ParseQuery(body)
}

type view struct {
	Title    string
	Message  string
	Request  *ddb.Request
	Token    string
	Notice   string
	Decision bool
}

func decide(req ddb.Request, token, notice string) view {
	return view{Title: req.Subject, Request: &req, Token: token, Notice: notice, Decision: true}
}

// The page is the whole interface: readable on a phone, no login, no script,
// nothing loaded from elsewhere. It must be enough to decide in under thirty
// seconds.
var pageTemplate = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>{{.Title}}</title>
<style>
body{font:17px/1.45 -apple-system,system-ui,sans-serif;margin:0;padding:20px 16px;background:#fafaf8;color:#1a1a1a;max-width:560px}
h1{font-size:21px;margin:0 0 12px}
.meta{color:#5a5a5a;font-size:14px;margin-bottom:14px}
.payload{white-space:pre-wrap;background:#fff;border:1px solid #ddd;border-radius:8px;padding:12px;margin-bottom:12px}
.ask{font-weight:600;margin-bottom:16px}
.notice{background:#fff4e5;border:1px solid #f0c27b;border-radius:8px;padding:10px;margin-bottom:12px}
textarea{width:100%;box-sizing:border-box;min-height:90px;font:inherit;padding:10px;border-radius:8px;border:1px solid #bbb}
.buttons{display:flex;gap:10px;margin-top:12px}
button{flex:1;font:inherit;font-weight:600;padding:14px;border-radius:8px;border:0;color:#fff}
.go{background:#1f6f43}.no{background:#9b2c2c}
footer{color:#6a6a6a;font-size:13px;margin-top:20px}
@media (prefers-color-scheme:dark){body{background:#151515;color:#eee}.payload{background:#1f1f1f;border-color:#333}.meta,footer{color:#aaa}textarea{background:#1f1f1f;color:#eee;border-color:#444}.notice{background:#3a2c14;border-color:#7a5a20}}
</style></head><body>
<h1>{{.Title}}</h1>
{{if .Decision}}
<div class="meta">{{.Request.Priority}} · raised by {{.Request.AgentID}}</div>
<div class="payload">{{.Request.Payload}}</div>
<div class="ask">Requested: {{.Request.RequestedResponse}}</div>
{{if .Notice}}<div class="notice">{{.Notice}}</div>{{end}}
<form method="post" action="/reply">
<input type="hidden" name="t" value="{{.Token}}">
<label for="text">Note (optional)</label>
<textarea id="text" name="text" maxlength="2000"></textarea>
<div class="buttons">
<button class="go" type="submit" name="decision" value="direction">Give direction</button>
<button class="no" type="submit" name="decision" value="denial">Deny</button>
</div>
</form>
<footer>Opening this page records nothing. Pressing a button records one answer. This link proves you hold it, not who you are.</footer>
{{else}}
<p>{{.Message}}</p>
{{end}}
</body></html>
`))

func page(status int, v view) events.APIGatewayV2HTTPResponse {
	var b strings.Builder
	if err := pageTemplate.Execute(&b, v); err != nil {
		return events.APIGatewayV2HTTPResponse{StatusCode: http.StatusInternalServerError, Body: "page failed to render"}
	}
	return events.APIGatewayV2HTTPResponse{
		StatusCode: status,
		Headers: map[string]string{
			"content-type":            "text/html; charset=utf-8",
			"cache-control":           "no-store",
			"referrer-policy":         "no-referrer",
			"x-content-type-options":  "nosniff",
			"content-security-policy": "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'",
		},
		Body: b.String(),
	}
}

func main() {
	h, err := initialize(context.Background())
	if err != nil {
		log.Fatalf("reply: init: %v", err)
	}
	lambda.Start(h.handle)
}

// initialize runs once per execution environment, in the Lambda init phase.
func initialize(ctx context.Context) (*replier, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	signingKey, err = signing.Load(ctx, secretsmanager.NewFromConfig(awsCfg), cfg.SigningKeyPath)
	if err != nil {
		return nil, err
	}
	return &replier{
		db:       dynamodb.NewFromConfig(awsCfg),
		sfn:      sfn.NewFromConfig(awsCfg),
		table:    cfg.AuditTableName,
		key:      signingKey,
		replyKey: replytoken.KeyFrom(signingKey),
		now:      time.Now,
	}, nil
}
