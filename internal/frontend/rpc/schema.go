package rpc

//go:generate go run ../../../cmd/nodwyn-schema -out ../../../docs/protocol.schema.json

import (
	"encoding/json"
	"path"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/frontend/run"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/prim"
	"github.com/fclaude/nodwyn/internal/redact"
)

type methodContract struct{ params, result any }

func contracts() map[string]methodContract {
	return map[string]methodContract{
		"run.start":                {runParams{}, runResult{}},
		"run.info":                 {idParams{}, run.State{}},
		"run.stop":                 {idParams{}, struct{}{}},
		"run.resume":               {resumeParams{}, runResult{}},
		"session.watch":            {struct{}{}, []sessionState{}},
		"hello":                    {helloParams{}, helloResult{}},
		"session.list":             {struct{}{}, []sessionInfo{}},
		"session.open":             {openParams{}, sessionDetails{}},
		"worktree.open":            {openParams{}, sessionDetails{}},
		"session.resume":           {idParams{}, sessionDetails{}},
		"session.model.set":        {modelSetParams{}, core.ModelState{}},
		"session.info":             {idParams{}, sessionDetails{}},
		"session.close":            {idParams{}, struct{}{}},
		"session.rename":           {renameParams{}, struct{}{}},
		"session.archive":          {archiveParams{}, struct{}{}},
		"session.delete":           {idParams{}, struct{}{}},
		"session.policy.read":      {policyReadParams{}, inspectionStarted{}},
		"session.policy.write":     {policyWriteParams{}, inspectionStarted{}},
		"session.friction":         {idParams{}, inspectionStarted{}},
		"session.eval":             {evalParams{}, struct{}{}},
		"session.send":             {sendParams{}, struct{}{}},
		"session.abort":            {idParams{}, struct{}{}},
		"session.subscribe":        {subscribeParams{}, subscription{}},
		"session.unsubscribe":      {idParams{}, struct{}{}},
		"log.read":                 {readParams{}, core.LogPage{}},
		"session.answer":           {answerParams{}, struct{}{}},
		"session.secret":           {secretParams{}, struct{}{}},
		"session.approval.create":  {approvalParams{}, struct{}{}},
		"session.binding.create":   {bindingParams{}, struct{}{}},
		"session.approve_all":      {approveAllParams{}, struct{}{}},
		"attention.list":           {struct{}{}, []attentionItem{}},
		"notes.read":               {notesParams{}, notesResult{}},
		"session.notes.write":      {notesWriteParams{}, inspectionStarted{}},
		"models.status":            {struct{}{}, []modelStatus{}},
		"models.offered":           {offeredParams{}, []string{}},
		"models.published":         {publishedParams{}, &publishedModel{}},
		"keychain.status":          {struct{}{}, keychainStatus{}},
		"keychain.create":          {keychainCreateParams{}, keychainStatus{}},
		"keychain.unlock":          {passphraseParams{}, keychainStatus{}},
		"keychain.passphrase":      {keychainPassphraseParams{}, keychainStatus{}},
		"credentials.list":         {struct{}{}, credentialsResult{}},
		"credentials.add":          {credentialAddParams{}, credentialsResult{}},
		"credentials.replace":      {credentialReplaceParams{}, credentialsResult{}},
		"credentials.remove":       {credentialRemoveParams{}, credentialRemoved{}},
		"credentials.bind":         {credentialBindParams{}, credentialsResult{}},
		"credentials.authorize":    {credentialAuthorizeParams{}, authorizationInfo{}},
		"settings.read":            {struct{}{}, settingsResult{}},
		"settings.control.write":   {controlParams{}, settingsResult{}},
		"settings.provider.write":  {providerParams{}, settingsResult{}},
		"settings.provider.remove": {nameParams{}, settingsResult{}},
		"settings.model.write":     {modelWriteParams{}, settingsResult{}},
		"settings.model.remove":    {nameParams{}, settingsResult{}},
		"models.test":              {testParams{}, testResult{}},
		"settings.docs.write":      {docsParams{}, settingsResult{}},
		"settings.write":           {settingsWriteParams{}, settingsResult{}},
		"workspace.list":           {struct{}{}, []workspaceInfo{}},
		"workspace.active":         {workspaceActiveParams{}, workspaceActiveResult{}},
		"workspace.save":           {workspaceSaveParams{}, workspaceInfo{}},
		"workspace.remove":         {workspacePathParams{}, struct{}{}},
		"versions.list":            {versionsParams{}, []prim.Version{}},
		"session.restore":          {restoreParams{}, inspectionStarted{}},
		"session.attach":           {attachParams{}, log.Attach{}},
		"mcp.list":                 {struct{}{}, []config.MCPServer{}},
		"mcp.inspect":              {mcpNameParams{}, mcpInspection{}},
		"mcp.save":                 {mcpSaveParams{}, config.MCPServer{}},
		"mcp.approve":              {mcpApproveParams{}, config.MCPServer{}},
		"mcp.remove":               {mcpNameParams{}, struct{}{}},
		"mcp.runtime":              {struct{}{}, mcpRuntime{}},
		"mcp.pull":                 {mcpNameParams{}, mcpPulled{}},
	}
}

// WireSchema generates JSON Schema from the actual wire and event payload
// types. It describes shape, not policy: prompt validity, bindings and approval
// scope are checked at runtime. Unknown response fields remain forward-compatible.
func WireSchema() ([]byte, error) {
	g := schemaBuilder{defs: map[string]any{}}
	id := map[string]any{"type": []string{"string", "number", "null"}}
	var requests []any
	names := make([]string, 0, len(contracts()))
	for name := range contracts() {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		contract := contracts()[name]
		paramType := reflect.TypeOf(contract.params)
		// Keep request strictness local: the same Go type may appear in an
		// extensible response or notification.
		paramSchema := map[string]any{
			"allOf": []any{g.typ(paramType)}, "unevaluatedProperties": false,
		}
		required := []string{"jsonrpc", "method"}
		if paramType.NumField() > 0 {
			required = append(required, "params")
		}
		request := map[string]any{"type": "object", "properties": map[string]any{
			"jsonrpc": map[string]any{"const": "2.0"}, "method": map[string]any{"const": name}, "id": id, "params": paramSchema,
		}, "required": required}
		key := "request." + name
		g.defs[key] = request
		requests = append(requests, map[string]any{"$ref": "#/$defs/" + key})
		g.defs["result."+name] = g.typ(reflect.TypeOf(contract.result))
	}
	g.defs["request"] = map[string]any{"anyOf": requests}
	g.defs["response"] = map[string]any{"type": "object", "properties": map[string]any{
		"jsonrpc": map[string]any{"const": "2.0"}, "id": id, "result": map[string]any{}, "error": g.typ(reflect.TypeOf(rpcError{})),
	}, "required": []string{"jsonrpc", "id"}, "oneOf": []any{
		map[string]any{"required": []string{"result"}, "not": map[string]any{"required": []string{"error"}}},
		map[string]any{"required": []string{"error"}, "not": map[string]any{"required": []string{"result"}}},
	}}
	payloads := map[string]any{
		"session.state":        sessionState{},
		event.InspectionResult: event.Inspection{},
		event.PolicyResult:     event.Policy{},
		event.StreamText:       event.Text{}, event.StreamThinking: event.Text{}, event.Output: event.Text{},
		event.Status: event.StatusInfo{}, event.Error: event.ErrorInfo{}, event.PromptConfirm: event.Confirm{},
		event.PromptCheckpoint: event.Checkpoint{}, event.PromptSecret: event.SecretPrompt{}, event.PromptClosed: event.Closed{},
		event.PromptChoice: event.Choice{}, string(log.KindSettingChange): log.SettingChange{},
		string(log.KindSessionStart): log.SessionStart{}, string(log.KindSessionResume): log.SessionStart{}, string(log.KindSessionEnd): log.SessionEnd{},
		string(log.KindEvalReq): log.EvalRequest{}, string(log.KindEvalResult): log.EvalResult{},
		string(log.KindUserMsg): log.UserMsg{}, string(log.KindHarnessMsg): log.HarnessMsg{},
		string(log.KindContextNarrow): log.ContextNarrow{}, string(log.KindContextWiden): log.ContextWiden{}, string(log.KindContextFold): log.ContextFold{}, string(log.KindContextCompact): log.ContextCompact{}, string(log.KindLLMReq): log.LLMReq{}, string(log.KindLLMResp): log.LLMResp{},
		string(log.KindToolCall): log.ToolCallEvent{}, string(log.KindToolResult): log.ToolResult{},
		string(log.KindGuardDecision): log.GuardDecision{}, string(log.KindApprovalChange): log.ApprovalChange{}, string(log.KindBindingChange): log.BindingChange{}, string(log.KindSummaryDrop): log.SummaryDrop{},
		string(log.KindEgressCheck): log.EgressCheck{}, string(log.KindGatePlan): log.GatePlan{}, string(log.KindPlanStep): log.PlanStep{}, string(log.KindJevReq): log.JevReq{},
		string(log.KindCredRegister): log.CredRegister{}, string(log.KindCredRefresh): log.CredRefresh{}, string(log.KindMCPEgress): log.MCPEgress{}, string(log.KindTurnCheckpoint): log.TurnCheckpoint{},
		string(log.KindRunState): log.RunState{}, string(log.KindRunTurn): log.RunTurn{}, string(log.KindRunCheckpoint): log.RunCheckpoint{},
		string(log.KindTurnEnd): log.TurnEnd{}, string(log.KindNote): log.Note{}, string(log.KindFileVersion): log.FileVersion{}, string(log.KindAttach): log.Attach{}, string(log.KindDocUpload): log.DocUpload{},
		string(log.KindSessionTitle): log.SessionTitle{}, string(log.KindSessionArchive): log.SessionArchive{}, string(log.KindSessionModel): log.SessionModel{},
		string(log.KindWorktreeCreate): log.WorktreeChange{}, string(log.KindWorktreeRestore): log.WorktreeChange{}, string(log.KindWorktreeRemove): log.WorktreeChange{},
		string(log.KindRedact): struct {
			Source uint64         `json:"source_seq"`
			Events []redact.Event `json:"events"`
		}{},
		// Summaries are a revocable view over a recorded sequence range.
		string(log.KindSummary): log.Summary{},
	}
	var variants []any
	kinds := make([]string, 0, len(payloads))
	for kind := range payloads {
		kinds = append(kinds, kind)
	}
	slices.Sort(kinds)
	for _, kind := range kinds {
		payload := g.typ(reflect.TypeOf(payloads[kind]))
		g.defs["payload."+kind] = payload
		ev := map[string]any{"type": "object", "properties": map[string]any{
			"id": map[string]any{"type": "string"}, "kind": map[string]any{"const": kind}, "seq": map[string]any{"type": "integer", "minimum": 0}, "payload": payload,
		}, "required": []string{"id", "kind", "seq", "payload"}}
		variants = append(variants, ev)
	}
	g.defs["sessionEvent"] = map[string]any{"oneOf": variants}
	g.defs["notification"] = map[string]any{"oneOf": []any{
		wireNotification("session.event", map[string]any{"$ref": "#/$defs/sessionEvent"}),
		wireNotification("session.closed", g.typ(reflect.TypeOf(idParams{}))),
	}}
	g.defs["message"] = map[string]any{"anyOf": []any{
		map[string]any{"$ref": "#/$defs/request"}, map[string]any{"$ref": "#/$defs/response"}, map[string]any{"$ref": "#/$defs/notification"},
	}}
	doc := map[string]any{"$schema": "https://json-schema.org/draft/2020-12/schema", "title": "nodwyn local protocol v1", "$comment": "Generated by go generate ./internal/frontend/rpc; do not edit.", "$defs": g.defs, "anyOf": []any{
		map[string]any{"$ref": "#/$defs/message"}, map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"$ref": "#/$defs/message"}},
	}}
	return json.MarshalIndent(doc, "", "  ")
}
func wireNotification(method string, params any) map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"jsonrpc": map[string]any{"const": "2.0"}, "method": map[string]any{"const": method}, "params": params}, "required": []string{"jsonrpc", "method", "params"}, "not": map[string]any{"required": []string{"id"}}}
}

type schemaBuilder struct{ defs map[string]any }

// writeOnly are the request fields that carry a secret into the daemon.
var writeOnly = map[string]bool{
	"secretParams.secret": true, "passphraseParams.passphrase": true, "controlParams.api_key": true, "providerParams.api_key": true, "mcpSaveParams.api_key": true,
	"keychainCreateParams.passphrase": true, "keychainPassphraseParams.current": true, "keychainPassphraseParams.passphrase": true,
	"credentialAddParams.value": true, "credentialReplaceParams.value": true, "credentialAuthorizeParams.client_secret": true,
}

func (g *schemaBuilder) typ(t reflect.Type) map[string]any {
	if t == reflect.TypeOf(json.RawMessage{}) || t.Kind() == reflect.Interface {
		return map[string]any{}
	}
	if t == reflect.TypeOf(time.Time{}) {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	if t.Kind() == reflect.Pointer {
		return map[string]any{"anyOf": []any{g.typ(t.Elem()), map[string]any{"type": "null"}}}
	}
	if t.Kind() == reflect.Struct && t.Name() != "" {
		key := path.Base(t.PkgPath()) + "." + t.Name()
		if _, ok := g.defs[key]; !ok {
			g.defs[key] = map[string]any{}
			g.defs[key] = g.object(t)
		}
		return map[string]any{"$ref": "#/$defs/" + key}
	}
	switch t.Kind() {
	case reflect.Struct:
		return g.object(t)
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer", "minimum": 0}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": []string{"array", "null"}, "items": g.typ(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": []string{"object", "null"}, "additionalProperties": g.typ(t.Elem())}
	default:
		panic("unsupported protocol schema type: " + t.String())
	}
}
func (g *schemaBuilder) object(t reflect.Type) map[string]any {
	props := map[string]any{}
	required := []string{}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		if field.Anonymous && field.Tag.Get("json") == "" {
			embedded := g.object(field.Type)
			for key, value := range embedded["properties"].(map[string]any) {
				props[key] = value
			}
			required = append(required, embedded["required"].([]string)...)
			continue
		}
		tag := strings.Split(field.Tag.Get("json"), ",")
		name := tag[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		p := g.typ(field.Type)
		if t == reflect.TypeOf(helloParams{}) && name == "protocol" {
			p["const"] = ProtocolVersion
		}
		if t == reflect.TypeOf(approvalParams{}) && name == "scope" {
			p["enum"] = []string{"session", "turn"}
		}
		if writeOnly[t.Name()+"."+name] {
			p["writeOnly"] = true
		}
		props[name] = p
		if !slices.Contains(tag, "omitempty") {
			required = append(required, name)
		}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}
