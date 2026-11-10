package capacity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nabutabu/crane-oss/pkg/api"
)

type attachmentStepFake struct{ phases []api.ZoneBootstrapPhase }

func (f *attachmentStepFake) Step(_ context.Context, _ api.Operation, phase api.ZoneBootstrapPhase) (api.BootstrapStepResult, error) {
	f.phases = append(f.phases, phase)
	switch phase {
	case api.ZoneInfrastructureProvisioning:
		return api.BootstrapStepResult{
			LaunchParameters: []byte(`{"image_id":"ami-pinned","instance_type":"m7i.xlarge","subnet_id":"subnet-new","security_group_ids":["sg-owned"],"iam_instance_profile":"deployment-worker","associate_public_ip_address":true}`),
			TemplateRevision: "attach-pinned", Scheduling: api.SchedulingTemplate{InstanceType: "m7i.xlarge", Architecture: "amd64", Region: "us-west-2", Zone: "us-west-2b"},
		}, nil
	case api.ZoneDominatorInstalling:
		return api.BootstrapStepResult{DominatorEndpoint: "https://10.96.0.42:44000", DominatorIdentity: "spiffe://crane.internal/dominator/attached-zone"}, nil
	default:
		return api.BootstrapStepResult{}, nil
	}
}

func (*attachmentStepFake) Validated(context.Context, api.Operation) (bool, error) { return true, nil }

func TestPostgresAttachmentRemainsEmptyUntilValidated(t *testing.T) {
	db := database(t)
	store := NewStore(db)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO host(id,zone,role,state,provider,providerid,protected) VALUES
 ('spire-owned','zone','spire','READY','aws','i-spire',true),
 ('platform-owned','zone','platform','READY','aws','i-platform',true);
 INSERT INTO deployment_runtime(cluster_id,deployment_id,provider,account_id,region,vpc_id,spire_host_id,platform_host_id,dominator_image,runtime_secret_name,trust_domain,pod_cidr)
 VALUES('cluster','deployment','aws','account','us-west-2','vpc-owned','spire-owned','platform-owned','dominator:pin','crane-runtime','crane.internal','10.244.0.0/16')`); err != nil {
		t.Fatal(err)
	}
	runtime, err := store.LoadDeploymentRuntime(ctx, "cluster")
	if err != nil {
		t.Fatal(err)
	}
	hosts, err := store.LoadDeploymentHosts(ctx, runtime)
	if err != nil || hosts.SpireInstanceID != "i-spire" || hosts.PlatformInstanceID != "i-platform" {
		t.Fatalf("shared hosts %+v: %v", hosts, err)
	}
	zone := api.ZoneInput{ID: "attached-zone", Provider: "aws", AccountID: "account", Region: "us-west-2", ProviderZoneID: "us-west-2b"}
	group := api.NodeGroupInput{Naming: api.NodeGroupNaming{Provider: "aws", DevelopmentStage: "test", Region: "us-west-2", AZ: "b", Group: "general"}, ClusterID: "cluster", ZoneID: zone.ID, NodePoolID: "attached-pool", ProviderZoneID: zone.ProviderZoneID, CapacityClass: "general", MinNodes: 0, MaxNodes: 4, TemplateRevision: "pending", PerNode: api.Resources{VCPU: 4, MemoryMiB: 8192}, LaunchParameters: []byte(`{}`)}
	if err := group.NormalizeID(); err != nil {
		t.Fatal(err)
	}
	intent := api.AttachmentIntent{SourceGroupID: "aws-test-us-west-2-a-general", Zone: zone,
		Membership: api.ClusterZoneInput{ClusterID: "cluster", ZoneID: zone.ID},
		Pool:       api.NodePoolInput{ID: "attached-pool", Limits: api.Resources{VCPU: 16, MemoryMiB: 32768, Nodes: 4}},
		Group:      group, ProviderConfig: []byte(`{"public_subnet_cidr":"10.0.4.0/24","private_subnet_cidr":"10.0.5.0/24"}`)}
	parameters, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	request := api.BootstrapRequest{Kind: "attach_zone", RequestID: "attach-request", ClusterID: "cluster", ZoneID: zone.ID, Parameters: parameters}
	route := NewAPI(store, "admin-test", nil)
	route.AttachmentsEnabled = true
	route.ValidateAttachment = func(_ context.Context, got api.AttachmentIntent, deployment DeploymentRuntime) error {
		if got.SourceGroupID != intent.SourceGroupID || deployment.VPCID != "vpc-owned" {
			t.Fatal("attachment validation lost trusted deployment context")
		}
		return nil
	}
	submit := func(in api.BootstrapRequest) *httptest.ResponseRecorder {
		body, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/bootstrap", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer admin-test")
		response := httptest.NewRecorder()
		route.Handler().ServeHTTP(response, req)
		return response
	}
	created := submit(request)
	if created.Code != http.StatusOK {
		t.Fatalf("attachment admission %d: %s", created.Code, created.Body.String())
	}
	var accepted map[string]string
	if err := json.Unmarshal(created.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	id := accepted["operation_id"]
	if id == "" {
		t.Fatal("attachment operation ID missing")
	}
	route.ValidateAttachment = func(context.Context, api.AttachmentIntent, DeploymentRuntime) error {
		t.Fatal("exact replay reached fallible provider discovery")
		return ErrInvalid
	}
	if replayHTTP := submit(request); replayHTTP.Code != http.StatusOK || !strings.Contains(replayHTTP.Body.String(), id) {
		t.Fatalf("HTTP replay %d: %s", replayHTTP.Code, replayHTTP.Body.String())
	}
	if replay, err := store.CreateBootstrap(ctx, request); err != nil || replay != id {
		t.Fatalf("replay %q: %v", replay, err)
	}
	if _, err := store.SetTarget(ctx, api.CapacityRequest{NodeGroupID: group.ID, ClusterID: "cluster", RequestID: "premature", DesiredTarget: 1, TemplateRevision: "pending"}); !errors.Is(err, ErrInactive) {
		t.Fatalf("premature capacity admission: %v", err)
	}
	fake := &attachmentStepFake{}
	for range 4 {
		controller := BootstrapController{Store: store, Executor: fake, Lease: time.Minute}
		if err := controller.RunAttachmentOnce(ctx, zone.ID, "api-controller"); err != nil {
			t.Fatal(err)
		}
	}
	var lifecycle, groupState string
	if err := db.QueryRowContext(ctx, `SELECT z.lifecycle_state,g.lifecycle_state FROM zones z JOIN node_groups g ON g.zone_id=z.id WHERE z.id=$1`, zone.ID).Scan(&lifecycle, &groupState); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "initializing" || groupState != "initializing" {
		t.Fatalf("premature activation: zone=%s group=%s", lifecycle, groupState)
	}
	if err := (&BootstrapController{Store: store, Executor: fake, Lease: time.Minute}).RunAttachmentOnce(ctx, zone.ID, "api-controller"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT z.lifecycle_state,g.lifecycle_state FROM zones z JOIN node_groups g ON g.zone_id=z.id WHERE z.id=$1`, zone.ID).Scan(&lifecycle, &groupState); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "ready" || groupState != "active" || len(fake.phases) != 4 {
		t.Fatalf("attachment state zone=%s group=%s phases=%v", lifecycle, groupState, fake.phases)
	}
	if _, err := store.SetTarget(ctx, api.CapacityRequest{NodeGroupID: group.ID, ClusterID: "cluster", RequestID: "after-ready", DesiredTarget: 1, TemplateRevision: "attach-pinned"}); err != nil {
		t.Fatal(err)
	}
}
