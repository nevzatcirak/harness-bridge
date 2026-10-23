package execution
import("context";"errors";"testing";"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode")
type fake struct{messages []opencode.Message;status string;sends int;fail bool}
func(f *fake)History(context.Context,string)([]opencode.Message,error){return f.messages,nil}
func(f *fake)Status(context.Context)(map[string]opencode.Status,error){return map[string]opencode.Status{"s":{Type:f.status}},nil}
func(f *fake)Prompt(context.Context,string,string,string)error{f.sends++;if f.fail{return errors.New("network")};return nil}
func(f *fake)Abort(context.Context,string)error{return nil}
func TestDuplicateAndBusy(t *testing.T){f:=&fake{status:"idle"};m:=New(f);ctx:=context.Background();a,e:=m.Send(ctx,"s","first","key");if e!=nil{t.Fatal(e)};b,e:=m.Send(ctx,"s","first","key");if e!=nil||a.ID!=b.ID||f.sends!=1{t.Fatal("duplicate")};if _,e=m.Send(ctx,"s","other","new-key");e==nil{t.Fatal("concurrent send accepted")}}
func TestMultiTurn(t *testing.T){f:=&fake{status:"idle"};m:=New(f);ctx:=context.Background();a,e:=m.Send(ctx,"s","first","key1");if e!=nil{t.Fatal(e)};msg:=opencode.Message{};msg.Info.ID="assistant1";msg.Info.Role="assistant";f.messages=[]opencode.Message{msg};x,e:=m.Get(ctx,a.ID);if e!=nil||x.State!="completed"{t.Fatalf("status %v %v",x,e)};b,e:=m.Send(ctx,"s","second","key2");if e!=nil||b.SessionID!=a.SessionID||f.sends!=2{t.Fatalf("same-session continuation: %v",e)}}
func TestUncertainSubmission(t *testing.T){f:=&fake{status:"idle",fail:true};m:=New(f);_,_=m.Send(context.Background(),"s","task","key");_,_=m.Send(context.Background(),"s","task","key");if f.sends!=1{t.Fatal("sent twice")}}
