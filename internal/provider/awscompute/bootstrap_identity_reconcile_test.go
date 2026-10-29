package awscompute

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIdentityRegistrationReconciliation(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	script, err := RenderIdentityScript(testConfig(), testInfra(), "i-0abcdef1234567890", "i-11111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	write("aws", "#!/bin/sh\nexit 0\n")
	write("docker", `#!/usr/bin/env python3
import json, os, sys
args=sys.argv[1:]
if 'healthcheck' in args: raise SystemExit(0)
args=args[args.index('entry')+1:]
def value(flag):
    return args[args.index(flag)+1] if flag in args else None
path=os.environ['ENTRIES']
entries=json.load(open(path)) if os.path.exists(path) else []
selectors=sorted(args[i+1] for i,a in enumerate(args) if a=='-selector')
if args[0]=='show':
    for i,e in enumerate(entries):
        if e['id']!=value('-spiffeID'): continue
        if value('-parentID') and e['parent']!=value('-parentID'): continue
        if '-matchSelectorsOn' in args and sorted(e['selectors'])!=selectors: continue
        print('Entry ID : entry-%d' % i)
elif args[0]=='create':
    entries.append({'id':value('-spiffeID'),'parent':value('-parentID'),'selectors':selectors})
    with open(path,'w') as f: json.dump(entries,f)
    if os.path.exists(os.environ['FAIL_AFTER_CREATE']): raise SystemExit(1)
else: raise SystemExit(2)
`)
	script = strings.Replace(script, `export PATH="/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"`, `export PATH="`+dir+`:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"`, 1)
	write("identity", script)
	path := filepath.Join(dir, "entries.json")
	run := func(ok bool) {
		t.Helper()
		cmd := exec.Command("bash", filepath.Join(dir, "identity"))
		cmd.Env = append(os.Environ(), "ENTRIES="+path, "FAIL_AFTER_CREATE="+filepath.Join(dir, "fail"))
		out, err := cmd.CombinedOutput()
		if (err == nil) != ok {
			t.Fatalf("success=%v want %v: %s", err == nil, ok, out)
		}
	}
	write("fail", "")
	run(false) // Simulate a lost response after the server persisted the entry.
	if err := os.Remove(filepath.Join(dir, "fail")); err != nil {
		t.Fatal(err)
	}
	run(true)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	run(true)
	retried, _ := os.ReadFile(path)
	if string(original) != string(retried) {
		t.Fatal("retry created duplicate entries")
	}
	type entry struct {
		ID        string   `json:"id"`
		Parent    string   `json:"parent"`
		Selectors []string `json:"selectors"`
	}
	var entries []entry
	if err := json.Unmarshal(original, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 {
		t.Fatalf("got %d registrations", len(entries))
	}
	for _, e := range entries {
		if strings.HasSuffix(e.ID, "/crane-api") || strings.Contains(e.ID, "/dominator/") {
			service := "crane-api"
			if strings.Contains(e.ID, "/dominator/") {
				service = "dominator"
			}
			if strings.Join(e.Selectors, ",") != "k8s:ns:crane-system,k8s:sa:"+service {
				t.Fatalf("incorrect service selectors: %v", e.Selectors)
			}
		}
	}
	for _, name := range []string{"parent", "namespace", "serviceaccount", "uid-only", "extra-selector", "duplicate"} {
		t.Run(name, func(t *testing.T) {
			var changed []entry
			if err := json.Unmarshal(original, &changed); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "parent":
				changed[0].Parent = "spiffe://crane.internal/untrusted"
			case "namespace":
				changed[0].Selectors = []string{"k8s:ns:other", "k8s:sa:dominator"}
			case "serviceaccount":
				changed[0].Selectors = []string{"k8s:ns:crane-system", "k8s:sa:other"}
			case "uid-only":
				changed[0].Selectors = []string{"unix:uid:10002"}
			case "extra-selector":
				changed[0].Selectors = append(changed[0].Selectors, "unix:uid:10002")
			case "duplicate":
				changed = append(changed, changed[0])
			}
			data, _ := json.Marshal(changed)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			run(false)
			after, _ := os.ReadFile(path)
			if string(after) != string(data) {
				t.Fatal("conflict mutated registrations")
			}
		})
	}
}
