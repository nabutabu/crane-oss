package autoscaler

import (
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
)

// Fail if wire bindings drift away from the pinned upstream release without an
// explicit compatibility update. The hashes are from upstream CA v1.35.0.
func TestPinnedUpstreamBindings(t *testing.T) {
	for file, want := range map[string]string{
		"externalgrpc.proto":      "0897df2cde8058f9412bb3484105fa9dadcdb8874afde6c6a7116e598c2f5fc2",
		"externalgrpc.pb.go":      "444d7864803963390316227a928f90285853eb7210b067352ec1194e374a8e71",
		"externalgrpc_grpc.pb.go": "c42c7787fc633385dcd981246ff804c9f3622d2e8146349cbc0c67026e00c294",
	} {
		data, err := os.ReadFile("protos/" + file)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != want {
			t.Fatalf("%s differs from pinned release: %s", file, got)
		}
	}
}
