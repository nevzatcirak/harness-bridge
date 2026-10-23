package opencode
import (
 "bytes"
 "context"
 "encoding/json"
 "errors"
 "fmt"
 "io"
 "net/http"
 "net/url"
 "strings"
 "time"
)
type Session struct { ID string `json:"id"`; Title string `json:"title"`; Directory string `json:"directory"`; ProjectID string `json:"projectID"` }
type Message struct { Info struct { ID string `json:"id"`; Role string `json:"role"`; SessionID string `json:"sessionID"`; Error json.RawMessage `json:"error,omitempty"` } `json:"info"`; Parts []json.RawMessage `json:"parts"` }
type Status struct { Type string `json:"type"` }
type Client struct { base *url.URL; http *http.Client; username,password string }
func New(base, username, password string) (*Client,error) {
 u,err:=url.Parse(base);if err!=nil{return nil,err}
 if u.Scheme!="http"&&u.Scheme!="https" {return nil,errors.New("invalid OpenCode URL scheme")}
 if u.User!=nil||u.RawQuery!=""||u.Fragment!="" {return nil,errors.New("unexpected URL components")}
 if u.Hostname()!="127.0.0.1"&&u.Hostname()!="localhost"&&u.Hostname()!="::1" {return nil,errors.New("OpenCode must use loopback")}
 if username=="" {username="opencode"}
 return &Client{base:u,http:&http.Client{Timeout:10*time.Second},username:username,password:password},nil
}
func (c *Client) do(ctx context.Context,method,path string,body,out any) error {
 u:=*c.base;u.Path=strings.TrimRight(c.base.Path,"/")+path
 var r io.Reader
 if body!=nil {b,err:=json.Marshal(body);if err!=nil{return err};r=bytes.NewReader(b)}
 req,err:=http.NewRequestWithContext(ctx,method,u.String(),r);if err!=nil{return err}
 if body!=nil {req.Header.Set("Content-Type","application/json")}
 if c.password!="" {req.SetBasicAuth(c.username,c.password)}
 res,err:=c.http.Do(req);if err!=nil{return err};defer res.Body.Close()
 if res.StatusCode<200||res.StatusCode>=300 {b,_:=io.ReadAll(io.LimitReader(res.Body,512));return fmt.Errorf("opencode HTTP %d: %s",res.StatusCode,string(b))}
 if out==nil{return nil}
 return json.NewDecoder(io.LimitReader(res.Body,8<<20)).Decode(out)
}
func(c *Client)List(ctx context.Context)([]Session,error){var s []Session;e:=c.do(ctx,"GET","/session",nil,&s);return s,e}
func(c *Client)Get(ctx context.Context,id string)(Session,error){var s Session;e:=c.do(ctx,"GET","/session/"+url.PathEscape(id),nil,&s);return s,e}
func(c *Client)History(ctx context.Context,id string)([]Message,error){var m []Message;e:=c.do(ctx,"GET","/session/"+url.PathEscape(id)+"/message",nil,&m);return m,e}
func(c *Client)Status(ctx context.Context)(map[string]Status,error){var s map[string]Status;e:=c.do(ctx,"GET","/session/status",nil,&s);return s,e}
func(c *Client)Prompt(ctx context.Context,id,prompt,messageID string)error{return c.do(ctx,"POST","/session/"+url.PathEscape(id)+"/prompt_async",map[string]any{"messageID":messageID,"parts":[]any{map[string]string{"type":"text","text":prompt}}},nil)}
func(c *Client)Abort(ctx context.Context,id string)error{return c.do(ctx,"POST","/session/"+url.PathEscape(id)+"/abort",nil,nil)}
func(c *Client)Diff(ctx context.Context,id string)(json.RawMessage,error){var d json.RawMessage;e:=c.do(ctx,"GET","/session/"+url.PathEscape(id)+"/diff",nil,&d);return d,e}
