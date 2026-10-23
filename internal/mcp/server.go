package mcp
import (
 "context"
 "crypto/subtle"
 "encoding/json"
 "errors"
 "fmt"
 "net/http"
 "strings"
 "github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
 "github.com/nevzatcirak/harness-bridge/internal/execution"
)
type Server struct{Client *opencode.Client; Runs *execution.Manager; Token string}
type request struct{JSONRPC string `json:"jsonrpc"`; ID json.RawMessage `json:"id"`; Method string `json:"method"`; Params json.RawMessage `json:"params"`}
type response struct{JSONRPC string `json:"jsonrpc"`; ID json.RawMessage `json:"id,omitempty"`; Result any `json:"result,omitempty"`; Error any `json:"error,omitempty"`}
type args struct{SessionID string `json:"session_id"`;ExecutionID string `json:"execution_id"`;Prompt string `json:"prompt"`;IdempotencyKey string `json:"idempotency_key"`}
func prop()map[string]any{return map[string]any{"type":"string"}}
func tool(name,desc string,required []string,properties map[string]any)map[string]any{return map[string]any{"name":name,"description":desc,"inputSchema":map[string]any{"type":"object","properties":properties,"required":required,"additionalProperties":false}}}
func tools()[]map[string]any{return []map[string]any{
 tool("list_sessions","List existing sessions",nil,map[string]any{}),
 tool("get_session","Inspect session",[]string{"session_id"},map[string]any{"session_id":prop()}),
 tool("read_session_history","Read existing session messages",[]string{"session_id"},map[string]any{"session_id":prop()}),
 tool("send_prompt","Submit prompt to the same existing session",[]string{"session_id","prompt","idempotency_key"},map[string]any{"session_id":prop(),"prompt":prop(),"idempotency_key":prop()}),
 tool("get_execution_status","Read execution state",[]string{"execution_id"},map[string]any{"execution_id":prop()}),
 tool("get_execution_result","Read execution output",[]string{"execution_id"},map[string]any{"execution_id":prop()}),
 tool("interrupt_execution","Request interruption",[]string{"execution_id"},map[string]any{"execution_id":prop()}),
}}
func native(s string)(string,error){if !strings.HasPrefix(s,"opencode:")||len(s)<=len("opencode:"){return "",errors.New("invalid_session_id")};return strings.TrimPrefix(s,"opencode:"),nil}
func(s *Server)call(ctx context.Context,name string,a args)(any,error){
 switch name {
 case "list_sessions":
  list,e:=s.Client.List(ctx);for i:=range list{list[i].ID="opencode:"+list[i].ID};return list,e
 case "get_session":id,e:=native(a.SessionID);if e!=nil{return nil,e};v,e:=s.Client.Get(ctx,id);if e==nil{v.ID="opencode:"+v.ID};return v,e
 case "read_session_history":id,e:=native(a.SessionID);if e!=nil{return nil,e};return s.Client.History(ctx,id)
 case "send_prompt":id,e:=native(a.SessionID);if e!=nil{return nil,e};return s.Runs.Send(ctx,id,a.Prompt,a.IdempotencyKey)
 case "get_execution_status":return s.Runs.Get(ctx,a.ExecutionID)
 case "get_execution_result":e,m,err:=s.Runs.Result(ctx,a.ExecutionID);return map[string]any{"execution":e,"messages":m},err
 case "interrupt_execution":return s.Runs.Interrupt(ctx,a.ExecutionID)
 default:return nil,fmt.Errorf("unknown tool %s",name)
 }
}
func(s *Server)ServeHTTP(w http.ResponseWriter,r *http.Request){
 if s.Token==""{http.Error(w,"server misconfigured",500);return}
 auth:=strings.TrimPrefix(r.Header.Get("Authorization"),"Bearer ")
 if len(auth)!=len(s.Token)||subtle.ConstantTimeCompare([]byte(auth),[]byte(s.Token))!=1{http.Error(w,"unauthorized",401);return}
 if r.Method!="POST"{http.Error(w,"method not allowed",405);return}
 r.Body=http.MaxBytesReader(w,r.Body,1<<20)
 var q request
 if err:=json.NewDecoder(r.Body).Decode(&q);err!=nil{http.Error(w,"invalid json",400);return}
 out:=response{JSONRPC:"2.0",ID:q.ID}
 switch q.Method {
 case "initialize":out.Result=map[string]any{"protocolVersion":"2025-03-26","capabilities":map[string]any{"tools":map[string]any{}},"serverInfo":map[string]string{"name":"harness-bridge","version":"0.1.0"}}
 case "notifications/initialized":w.WriteHeader(http.StatusAccepted);return
 case "ping":out.Result=map[string]any{}
 case "tools/list":out.Result=map[string]any{"tools":tools()}
 case "tools/call":
  var p struct{Name string `json:"name"`; Arguments args `json:"arguments"`}
  if err:=json.Unmarshal(q.Params,&p);err!=nil{out.Error=map[string]any{"code":-32602,"message":"invalid arguments"};break}
  result,err:=s.call(r.Context(),p.Name,p.Arguments)
  if err!=nil{out.Result=map[string]any{"isError":true,"content":[]any{map[string]string{"type":"text","text":err.Error()}}}}else{b,_:=json.Marshal(result);out.Result=map[string]any{"content":[]any{map[string]string{"type":"text","text":string(b)}}}}
 default:out.Error=map[string]any{"code":-32601,"message":"method not found"}
 }
 w.Header().Set("Content-Type","application/json");_ =json.NewEncoder(w).Encode(out)
}
