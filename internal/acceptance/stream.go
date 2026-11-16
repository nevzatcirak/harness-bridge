package acceptance

import (
	"bufio"
	"bytes"
	"sort"
	"strings"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
)

// StreamInfo is what the driver keeps of one recorded NDJSON stream: shapes and counts, never text.
type StreamInfo struct {
	InitSeen      bool
	MCPServers    int
	MCPKnown      bool
	APIKeySource  string
	Version       string
	ResultSeen    bool
	ResultSubtype string
	ResultIsError bool
	ToolUses      int
	// TrailingTypes counts the event types after the LAST result line ("undecodable" for a line
	// that does not decode), the capture ACCEPTANCE_S1 asks for.
	TrailingTypes map[string]int
}

// ParseStream reads a recorded stream.
func ParseStream(b []byte) StreamInfo {
	info := StreamInfo{TrailingTypes: map[string]int{}}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), 2<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		e := claudecode.DecodeLine(line)
		kind := e.Type
		if e.Undecodable {
			kind = "undecodable"
		} else if e.Subtype != "" {
			kind += "/" + e.Subtype
		}
		if info.ResultSeen {
			info.TrailingTypes[kind]++
		}
		if e.Undecodable {
			continue
		}
		switch {
		case e.Type == "system" && e.Subtype == "init" && !info.InitSeen && e.Init != nil:
			info.InitSeen = true
			info.MCPServers, info.MCPKnown = e.Init.MCPServers, e.Init.MCPServersKnown
			info.APIKeySource, info.Version = e.Init.APIKeySource, e.Init.Version
		case e.Type == "result" && e.Result != nil:
			info.ResultSeen, info.ResultSubtype = true, e.Result.Subtype
			info.ResultIsError = e.Result.IsError != nil && *e.Result.IsError
			info.TrailingTypes = map[string]int{} // only what follows the last result counts
		}
		for _, bl := range e.Blocks {
			if bl.Type == "tool_use" {
				info.ToolUses++
			}
		}
	}
	return info
}

// TrailingList renders the trailing types as "type:n,type:n" (sorted; "none" when empty).
func (s StreamInfo) TrailingList() string {
	if len(s.TrailingTypes) == 0 {
		return "none"
	}
	var out []string
	for k, n := range s.TrailingTypes {
		out = append(out, strings.ReplaceAll(k, " ", "_")+":"+itoa(n))
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for ; n > 0; n /= 10 {
		d = append([]byte{byte('0' + n%10)}, d...)
	}
	return string(d)
}
