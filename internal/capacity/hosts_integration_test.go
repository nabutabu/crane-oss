package capacity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/nabutabu/crane-oss/pkg/api"
)

func TestPostgresHostListingPlacementAndProviderFilters(t *testing.T) {
	db := database(t)
	mustExec(t, db, `
INSERT INTO zones(id,provider,account_id,region,provider_zone_id) VALUES
 ('east','aws','account','us-east-1','us-east-1a'),
 ('west-second','aws','another-account','us-west-2','us-west-2b'),
 ('another-provider','other','account','us-west-2','zone-a');
INSERT INTO host(id,zone,state,provider,providerid,protected,secretarn) VALUES
 ('a/bootstrap','zone','READY','aws','platform-id',true,'synthetic-hidden-reference'),
 ('c/terminated','zone','TERMINATED','aws','terminated-id',false,NULL),
 ('d/second-zone','west-second','READY','aws','second-id',false,NULL),
 ('excluded-east','east','READY','aws','east-id',false,NULL),
 ('excluded-provider','another-provider','READY','other','other-id',false,NULL),
 ('excluded-inconsistent','another-provider','READY','aws','inconsistent-id',false,NULL),
 ('excluded-unplaced',NULL,'READY','aws','unplaced-id',false,NULL);
INSERT INTO host(id,zone,crane_zone,node_group_id,node_pool_id,state,provider,providerid)
 VALUES('b/worker','east','zone','group','pool','READY','aws','worker-id');`)
	for _, tc := range []struct {
		provider, region string
		want             []string
	}{
		{"aws", "us-west-2", []string{"a/bootstrap", "b/worker", "c/terminated", "d/second-zone"}},
		{"aws", "us-east-1", []string{"excluded-east"}},
		{"other", "us-west-2", []string{"excluded-provider"}},
		{"unknown", "us-west-2", []string{}},
	} {
		r := httptest.NewRequest(http.MethodGet, "/v1/hosts?provider="+tc.provider+"&region="+tc.region, nil)
		r.Header.Set("Authorization", "Bearer admin")
		w := httptest.NewRecorder()
		NewAPI(NewStore(db), "admin", "", "").Handler().ServeHTTP(w, r)
		var result api.HostList
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		ids := make([]string, 0, len(result.Hosts))
		for _, host := range result.Hosts {
			ids = append(ids, host.ID)
			if host.Region != tc.region || host.Provider != tc.provider {
				t.Fatal("incorrect placement")
			}
		}
		if !reflect.DeepEqual(ids, tc.want) {
			t.Fatal(ids, tc.want)
		}
		if strings.Contains(w.Body.String(), "synthetic-hidden") || strings.Contains(w.Body.String(), "secretarn") {
			t.Fatal("internal connection fields exposed")
		}
	}
}
