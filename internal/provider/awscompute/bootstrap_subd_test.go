package awscompute

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nabutabu/crane-oss/internal/bootstrap"
)

func TestSubdNetworkRulesAreCreatedAndAcceptedOnRetry(t *testing.T) {
	groups := map[string]string{"spire": "sg-spire", "spire-nlb": "sg-spire-nlb", "platform": "sg-platform", "rds": "sg-rds"}
	permissions := map[string][]any{}
	cfg := testConfig()
	aws := &fakeAWS{call: func(_, op string, input any) (any, error) {
		in := input.(map[string]any)
		switch op {
		case "describe-security-groups":
			var kind string
			for _, raw := range in["Filters"].([]any) {
				f := raw.(map[string]any)
				if f["Name"] == "group-name" {
					kind = strings.TrimPrefix(f["Values"].([]string)[0], cfg.Name+"-")
				}
			}
			return map[string]any{"SecurityGroups": []any{map[string]any{"GroupId": groups[kind], "Tags": resourceTags(cfg.Name, kind), "IpPermissions": permissions[groups[kind]]}}}, nil
		case "authorize-security-group-ingress":
			id := in["GroupId"].(string)
			permissions[id] = append(permissions[id], in["IpPermissions"].([]any)...)
			return nil, &AWSError{Code: "InvalidPermission.Duplicate"}
		default:
			return nil, fmt.Errorf("unexpected operation %s", op)
		}
	}}
	for i := 0; i < 2; i++ {
		if _, err := ensureSecurityGroups(context.Background(), aws, cfg, testInfra()); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []struct {
		target, source string
		port           int32
	}{{"sg-spire", "sg-platform", 44001}, {"sg-spire-nlb", "sg-spire", 8081}} {
		found := false
		for _, raw := range permissions[want.target] {
			p := raw.(map[string]any)
			source := p["UserIdGroupPairs"].([]map[string]string)[0]["GroupId"]
			if p["IpProtocol"] == "tcp" && p["FromPort"] == want.port && source == want.source {
				found = true
			}
		}
		if !found {
			t.Fatal("missing private host rule", want)
		}
	}
}

func TestIdentityRegistrationRerunAndConflict(t *testing.T) {
	script, err := RenderIdentityScript(testConfig(), testInfra(), testAgentHosts()[1].ID, testAgentHosts()[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.sh")
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	harness := `aws(){ return 0; }
sleep(){ :; }
docker(){
  if [ "$4" = healthcheck ]; then return 0; fi
  while [ "$1" != entry ]; do shift; done
  shift; local operation="$1"; shift
  local id='' parent='' selector=''
  while [ "$#" -gt 0 ]; do
    case "$1" in
      -spiffeID) id="$2"; shift 2;;
      -parentID) parent="$2"; shift 2;;
      -selector) selector="$2"; shift 2;;
      *) shift;;
    esac
  done
  local file="` + dir + `/${id//\//_}"
  if [ "$operation" = create ]; then
    test ! -f "$file" || return 1
    printf '%s\n%s\n' "$parent" "$selector" > "$file"
    echo create >> '` + dir + `/creates'
  elif [ -f "$file" ]; then
    if [ -z "$parent" ] || { [ "$(sed -n '1p' "$file")" = "$parent" ] && [ "$(sed -n '2p' "$file")" = "$selector" ]; }; then echo 'Entry ID: test'; fi
  fi
}
source ` + shellQuote(path)
	for i := 0; i < 2; i++ {
		if out, err := exec.Command("bash", "-c", harness).CombinedOutput(); err != nil {
			t.Fatalf("registration run %d: %v %s", i, err, out)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "creates"))
	if err != nil || strings.Count(string(data), "create") != 5 {
		t.Fatal("registrations duplicated", err, string(data))
	}
	entry := filepath.Join(dir, "spiffe:__crane.internal_subd_"+testAgentHosts()[0].ID)
	if err := os.WriteFile(entry, []byte("wrong-parent\nsystemd:id:subd.service\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("bash", "-c", harness).CombinedOutput(); err == nil {
		t.Fatal("mismatched parent accepted", string(out))
	}
}

func TestSubdConfigurationRerun(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq required")
	}
	h := testAgentHosts()[0]
	script, err := RenderSubdScript(testConfig(), testInfra(), h)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, path := range []string{"bin", "etc", "run"} {
		if err := os.MkdirAll(filepath.Join(dir, path), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "bin/subd"), []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	conf := `    server_address = "` + testInfra().SpireAddress + `"
    trust_domain = "crane.internal"
WorkloadAttestor "systemd" {}
`
	if err := os.WriteFile(filepath.Join(dir, "etc/agent.conf"), []byte(conf), 0600); err != nil {
		t.Fatal(err)
	}
	replacer := strings.NewReplacer("/usr/local/bin", filepath.Join(dir, "bin"), "/etc/spire", filepath.Join(dir, "etc"), "/run/crane/subd", filepath.Join(dir, "run"))
	path := filepath.Join(dir, "subd.sh")
	if err := os.WriteFile(path, []byte(replacer.Replace(script)), 0600); err != nil {
		t.Fatal(err)
	}
	harness := `spire-agent(){ return 0; }
chown(){ :; }
systemctl(){
  case "$1" in
    show) if [ "$4" = MainPID ]; then echo 42; else echo invocation-test; fi;;
    restart) if [ "$2" = subd.service ]; then printf '%s' '{"ready":true,"pid":42,"invocation_id":"invocation-test","identity":"spiffe://crane.internal/subd/` + h.ID + `"}' > '` + filepath.Join(dir, "run/status.json") + `'; fi;;
  esac
}
source ` + shellQuote(path)
	for i := 0; i < 2; i++ {
		if out, err := exec.Command("bash", "-c", harness).CombinedOutput(); err != nil {
			t.Fatalf("configuration run %d: %v %s", i, err, out)
		}
	}
	config := filepath.Join(dir, "bin/appsettings.json")
	info, err := os.Stat(config)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("config permissions", err)
	}
	data, err := os.ReadFile(config)
	if err != nil || !strings.Contains(string(data), h.PrivateIP+":44001") || strings.Contains(string(data), "Token") {
		t.Fatal("invalid deployment config", err)
	}
}

func TestSubdFailureStopsBootstrapBeforeReadiness(t *testing.T) {
	s := &bootstrapSession{cfg: testConfig(), infra: testInfra(), spire: host{ID: testAgentHosts()[0].ID, PrivateIP: testAgentHosts()[0].PrivateIP}, platform: host{ID: testAgentHosts()[1].ID, PrivateIP: testAgentHosts()[1].PrivateIP}}
	sent := 0
	s.client = &fakeAWS{call: func(_, op string, input any) (any, error) {
		switch op {
		case "send-command":
			sent++
			return map[string]any{"Command": map[string]string{"CommandId": "failed-subd"}}, nil
		case "get-command-invocation":
			return map[string]string{"Status": "Failed"}, nil
		default:
			return nil, fmt.Errorf("unexpected operation %s", op)
		}
	}}
	if err := s.RunHostPhase(context.Background(), bootstrap.ValidateHandoff, bootstrap.NewReporter(io.Discard, false)); err == nil || sent != 1 {
		t.Fatal("bootstrap continued after failed Subd configuration", err, sent)
	}
}
