package main

import (
 "errors"
 "fmt"
 "os"
 "path/filepath"
 "strings"
)

// readSecret accepts an environment value OR an absolute path to a file,
// never both. It avoids accidentally falling back to an empty credential.
func readSecret(env func(string) string, name string) (string,error) {
 direct, path := env(name), env(name+"_FILE")
 if direct != "" && path != "" { return "",fmt.Errorf("%s and %s_FILE are mutually exclusive",name,name) }
 if path == "" { return direct,nil }
 if !filepath.IsAbs(path) { return "",fmt.Errorf("%s_FILE must be an absolute path",name) }
 info,err:=os.Lstat(path)
 if err!=nil { return "",fmt.Errorf("%s_FILE inaccessible: %w",name,err) }
 if !info.Mode().IsRegular() || info.Mode().Perm()&0077!=0 { return "",fmt.Errorf("%s_FILE must be a private regular file",name) }
 if info.Size()<1 || info.Size()>16384 {return "",fmt.Errorf("%s_FILE size invalid",name)}
 b,err:=os.ReadFile(path)
 if err!=nil {return "",fmt.Errorf("%s_FILE unreadable: %w",name,err)}
 value:=strings.TrimSuffix(strings.TrimSuffix(string(b),"\n"),"\r")
 if value=="" || strings.ContainsAny(value,"\r\n\x00") {return "",errors.New(name+"_FILE contains invalid secret")}
 return value,nil
}
